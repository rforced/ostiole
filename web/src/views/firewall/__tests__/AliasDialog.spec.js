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
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
  CountryPicker: true,
}

function open(alias = null, feeds = []) {
  const config = useConfigStore()
  config.replaceDraft({
    version: 6,
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
    expect(wrapper.find('#alias-url').exists()).toBe(false)
    expect(wrapper.find('#alias-refresh').exists()).toBe(true)
    await wrapper.get('#alias-entries').setValue('as15169\n15169, AS36040')
    await wrapper.get('form').trigger('submit')
    expect(config.aliases).toEqual([
      { name: 'google', type: 'asn', entries: ['AS15169', 'AS36040'], refreshHours: 24 },
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

// What Oracle's list offers, cut down.
const ORACLE = 'https://docs.oracle.com/en-us/iaas/tools/public_ip_ranges.json'
const oracleChoices = [
  { field: 'region', values: ['eu-frankfurt-1', 'us-ashburn-1'] },
  { field: 'tags', values: ['OBJECT_STORAGE', 'OCI', 'OSN'] },
]

describe('AliasDialog JSON lists', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.mocked(api.aliases.inspect).mockReset()
  })

  /** Whether the filter is on show. */
  const filterShown = (wrapper) => wrapper.text().includes('Keep only')

  // A typed URL is read before anything is saved, and what it can be
  // narrowed by is offered at once.
  it('reads a typed URL and offers what it can be narrowed by', async () => {
    vi.mocked(api.aliases.inspect).mockResolvedValue({
      source: ORACLE,
      entries: 1327,
      choices: oracleChoices,
    })
    const { wrapper, config } = open()
    await wrapper.get('#alias-name').setValue('oracle')
    expect(filterShown(wrapper)).toBe(false)
    await wrapper.get('#alias-url').setValue(ORACLE)
    await wrapper.get('#alias-url').trigger('change')
    expect(api.aliases.inspect).toHaveBeenCalledWith(ORACLE)
    await flushPromises()
    expect(filterShown(wrapper)).toBe(true)

    const tick = (value) =>
      wrapper
        .findAll('label')
        .find((l) => l.text() === value)
        .get('input')
        .setValue(true)
    await tick('us-ashburn-1')
    await tick('OCI')
    await wrapper.get('form').trigger('submit')
    expect(config.aliases).toEqual([
      {
        name: 'oracle',
        type: 'hosts',
        entries: [],
        url: ORACLE,
        refreshHours: 24,
        select: ['region=us-ashburn-1', 'tags=OCI'],
      },
    ])
  })

  it('stays hidden for a list with nothing to select', async () => {
    vi.mocked(api.aliases.inspect).mockResolvedValue({ source: 'x', entries: 22, choices: [] })
    const { wrapper } = open()
    await wrapper.get('#alias-url').setValue('https://api.cloudflare.com/client/v4/ips')
    await wrapper.get('#alias-url').trigger('change')
    await flushPromises()
    expect(filterShown(wrapper)).toBe(false)
  })

  it('says why it could not read a list', async () => {
    vi.mocked(api.aliases.inspect).mockRejectedValue(new ApiError(400, 'HTTP 404'))
    const { wrapper } = open()
    await wrapper.get('#alias-url').setValue('https://example.test/gone.json')
    await wrapper.get('#alias-url').trigger('change')
    await flushPromises()
    expect(wrapper.text()).toContain('Could not read the list: HTTP 404')
    expect(filterShown(wrapper)).toBe(false)
  })

  // An alias the router has fetched already says what it can be narrowed
  // by in its status, so opening it asks the router for nothing.
  it('uses the last fetch of a saved alias without reading the list again', () => {
    const alias = { name: 'oracle', type: 'hosts', entries: [], url: ORACLE, select: ['tags=OCI'] }
    const feeds = [
      { alias: 'oracle', parts: [{ source: ORACLE, entries: 615, choices: oracleChoices }] },
    ]
    const { wrapper } = open(alias, feeds)
    expect(api.aliases.inspect).not.toHaveBeenCalled()
    expect(filterShown(wrapper)).toBe(true)
    const oci = wrapper.findAll('label').find((l) => l.text() === 'OCI')
    expect(oci.get('input').element.checked).toBe(true)
  })

  // A saved selection stays on show when the list offers nothing, so it
  // can be removed, and is dropped when the URL goes.
  it('keeps a selection visible, and drops it with the URL', async () => {
    const alias = {
      name: 'oracle',
      type: 'hosts',
      entries: ['203.0.113.9'],
      url: ORACLE,
      select: ['region=us-*'],
    }
    const feeds = [{ alias: 'oracle', parts: [{ source: ORACLE, entries: 0 }] }]
    const { wrapper, config } = open(alias, feeds)
    expect(filterShown(wrapper)).toBe(true)
    expect(wrapper.text()).toContain('region=us-*')

    await wrapper.get('#alias-url').setValue('')
    expect(filterShown(wrapper)).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(config.aliases).toEqual([{ name: 'oracle', type: 'hosts', entries: ['203.0.113.9'] }])
  })

  it('reads nothing for somebody who cannot change the alias', async () => {
    const { wrapper } = open()
    useAuthStore().user = { username: 'v', role: 'viewer' }
    await wrapper.get('#alias-url').setValue(ORACLE)
    await wrapper.get('#alias-url').trigger('change')
    expect(api.aliases.inspect).not.toHaveBeenCalled()
  })
})
