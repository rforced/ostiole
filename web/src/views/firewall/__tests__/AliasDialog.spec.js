import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import AliasDialog from '@/views/firewall/AliasDialog.vue'

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual('@/lib/api')
  return { ...actual, api: { ...actual.api, aliases: { ...actual.api.aliases, inspect: vi.fn() } } }
})

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
  CountryPicker: true,
}

function open(alias = null, feeds = []) {
  const config = useConfigStore()
  config.replaceDraft({
    version: 11,
    zones: [],
    interfaces: [],
    aliases: alias ? [alias] : [],
    rules: [],
    nat: { outbound: { mode: 'automatic' } },
  })
  const wrapper = mount(AliasDialog, { props: { open: true, alias, feeds }, global: { stubs } })
  return { wrapper, config }
}

describe('AliasDialog AS numbers', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // The numbers are keys the router expands, so there is no URL to give,
  // and the schedule that refetches them is shown instead.
  it('saves the numbers in one spelling, without repeats', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#alias-name').setValue('google')
    await wrapper.get('#alias-type').setValue('asn')
    expect(wrapper.find('#alias-refresh').exists()).toBe(true)
    await wrapper.get('#alias-entries').setValue('as15169\n15169, AS36040')
    await wrapper.get('form').trigger('submit')
    expect(config.aliases).toEqual([
      { name: 'google', type: 'asn', entries: ['AS15169', 'AS36040'] },
    ])
  })

  it('refuses what is not an AS number, and an empty list', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#alias-name').setValue('google')
    await wrapper.get('#alias-type').setValue('asn')
    await wrapper.get('#alias-entries').setValue('AS15169\ngoogle')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role=alert]').text()).toContain('google is not an AS number')

    await wrapper.get('#alias-entries').setValue('')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role=alert]').text()).toContain('at least one AS number')
    expect(config.aliases).toEqual([])
  })

  it('shows what each number held at the last fetch', () => {
    const alias = { name: 'google', type: 'asn', entries: ['AS15169', 'AS64512'] }
    const feeds = [
      {
        alias: 'google',
        parts: [
          { source: 'x', asn: 'AS15169', holder: 'GOOGLE - Google LLC', entries: 1415 },
          { source: 'y', asn: 'AS64512', entries: 0 },
        ],
      },
    ]
    const { wrapper } = open(alias, feeds)
    expect(wrapper.get('#alias-entries').element.value).toBe('AS15169\nAS64512')
    const text = wrapper.text()
    expect(text).toContain('AS15169 · GOOGLE - Google LLC · 1,415 prefixes')
    expect(text).toContain('AS64512 · 0 prefixes')
  })
})

const LIST = 'https://lists.example.net/drop.txt'
const RANGES = 'https://ranges.example.net/public_ip_ranges.json'

describe('AliasDialog URL lines', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.mocked(api.aliases.inspect).mockReset()
  })

  // Each URL line is read when the box is left, and what it holds shows
  // under the box, one line per URL.
  it('reads each URL line and says how much it holds', async () => {
    const pending = []
    vi.mocked(api.aliases.inspect).mockImplementation(
      (url) =>
        new Promise((resolve) =>
          pending.push(() => resolve({ source: url, entries: url === LIST ? 1 : 1327 })),
        ),
    )
    const { wrapper, config } = open()
    await wrapper.get('#alias-name').setValue('cloud')
    expect(wrapper.find('#alias-refresh').exists()).toBe(false)
    await wrapper.get('#alias-entries').setValue(`203.0.113.9\n${LIST}\n${RANGES}`)
    expect(wrapper.find('#alias-refresh').exists()).toBe(true)
    await wrapper.get('#alias-entries').trigger('blur')
    expect(api.aliases.inspect).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('Reading the list…')
    pending.forEach((answer) => answer())
    await flushPromises()
    const lines = wrapper.findAll('li').map((l) => l.text())
    expect(lines).toEqual([`${LIST} · 1 address`, `${RANGES} · 1,327 addresses`])

    // Leaving the box again reads nothing twice.
    await wrapper.get('#alias-entries').trigger('blur')
    expect(api.aliases.inspect).toHaveBeenCalledTimes(2)

    await wrapper.get('#alias-refresh').setValue(6)
    await wrapper.get('form').trigger('submit')
    expect(config.aliases).toEqual([
      { name: 'cloud', type: 'hosts', entries: ['203.0.113.9', LIST, RANGES], refreshHours: 6 },
    ])
  })

  it('says why it could not read a list', async () => {
    vi.mocked(api.aliases.inspect).mockRejectedValue(new ApiError(400, 'HTTP 404'))
    const { wrapper } = open()
    await wrapper.get('#alias-entries').setValue('https://example.test/gone.json')
    await wrapper.get('#alias-entries').trigger('blur')
    await flushPromises()
    expect(wrapper.text()).toContain('Could not read the list: HTTP 404')
  })

  // A URL may hold a comma, which would split any other line.
  it('keeps a URL line whole, commas and all', async () => {
    const url = 'https://lists.example.net/drop?families=v4,v6'
    const { wrapper, config } = open()
    await wrapper.get('#alias-name').setValue('drop')
    await wrapper.get('#alias-entries').setValue(`${url}\n192.0.2.1, 192.0.2.2`)
    await wrapper.get('form').trigger('submit')
    expect(config.aliases).toEqual([
      { name: 'drop', type: 'hosts', entries: [url, '192.0.2.1', '192.0.2.2'] },
    ])
  })

  // The last fetch of a saved alias says what each URL held, so opening it
  // asks the router for nothing.
  it('uses the last fetch of a saved alias without reading the lists again', () => {
    const alias = { name: 'cloud', type: 'hosts', entries: [LIST, RANGES] }
    const feeds = [
      {
        alias: 'cloud',
        parts: [
          { source: LIST, entries: 3 },
          { source: RANGES, entries: 615 },
        ],
      },
    ]
    const { wrapper } = open(alias, feeds)
    expect(api.aliases.inspect).not.toHaveBeenCalled()
    expect(wrapper.findAll('li').map((l) => l.text())).toEqual([
      `${LIST} · 3 addresses`,
      `${RANGES} · 615 addresses`,
    ])
  })

  it('reads a saved URL line the router has not fetched yet', async () => {
    vi.mocked(api.aliases.inspect).mockResolvedValue({ source: LIST, entries: 12 })
    const { wrapper } = open({ name: 'cdn', type: 'hosts', entries: [LIST] })
    expect(api.aliases.inspect).toHaveBeenCalledWith(LIST)
    await flushPromises()
    expect(wrapper.text()).toContain(`${LIST} · 12 addresses`)
  })

  it('reads nothing for somebody who cannot change the alias', async () => {
    const { wrapper } = open()
    useAuthStore().user = { username: 'v', role: 'viewer' }
    await wrapper.get('#alias-entries').setValue(LIST)
    await wrapper.get('#alias-entries').trigger('blur')
    expect(api.aliases.inspect).not.toHaveBeenCalled()
  })
})

describe('AliasDialog round trip', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens a fetched alias as saved and leaves an untouched save alone', async () => {
    vi.mocked(api.aliases.inspect).mockResolvedValue({ source: 'x', entries: 12 })
    const { wrapper, config } = open({
      name: 'cdn',
      type: 'hosts',
      entries: ['198.51.100.7', 'https://lists.example.net/cdn.txt?families=v4,v6'],
    })
    config.saved = JSON.parse(JSON.stringify(config.draft))
    expect(wrapper.get('#alias-refresh').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
