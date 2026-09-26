import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import QueriesTab from '@/views/services/dns/QueriesTab.vue'

vi.mock('@/lib/api', () => ({
  api: {
    queries: { list: vi.fn(), summary: vi.fn(), clear: vi.fn() },
    blocking: { status: vi.fn(), lookup: vi.fn() },
  },
  ApiError: class ApiError extends Error {},
}))

/** A log of 450 answers, newest first, read 200 at a time like the server. */
function answers({ before } = {}) {
  const all = Array.from({ length: 450 }, (_, i) => ({
    seq: 450 - i,
    time: '2026-09-24T12:00:00Z',
    client: '10.0.0.2',
    name: `name${450 - i}.example`,
    type: 'A',
    status: 'ok',
  }))
  const from = before ? all.findIndex((e) => e.seq < before) : 0
  return { enabled: true, total: all.length, entries: all.slice(from, from + 200) }
}

/** The names in the table, top to bottom. */
function names(wrapper) {
  return wrapper
    .findAll('tbody tr')
    .map((tr) => tr.findAll('td')[2]?.text())
    .filter(Boolean)
}

function button(wrapper, label) {
  return wrapper.findAll('button').find((b) => b.text() === label)
}

/** A blocked answer, one answered, one the router answers for, and a service name. */
function mixed() {
  const row = (seq, name, status, extra = {}) => ({
    seq,
    time: '2026-09-24T12:00:00Z',
    client: '10.0.0.2',
    name,
    type: 'A',
    status,
    ...extra,
  })
  return {
    enabled: true,
    total: 4,
    entries: [
      row(4, 'ads.example.com', 'blocked', { lists: ['oisd'] }),
      row(3, 'example.com', 'ok', { answer: '93.184.216.34' }),
      row(2, 'nas.lan', 'ok', { answer: '10.0.0.5' }),
      row(1, '_dns.resolver.arpa', 'nodata'),
    ],
  }
}

/** A saved router with the query log on and no exceptions yet. */
function saved() {
  const config = useConfigStore()
  config.replaceDraft({
    version: 7,
    services: {
      dhcp: { enabled: false },
      dns: { enabled: true, domain: 'lan', queryLog: { enabled: true } },
    },
    blocking: { enabled: true, enforce: {} },
  })
  config.markSaved()
  config.loaded = true
  return config
}

function toggle(wrapper, label) {
  return wrapper.find(`input[aria-label="${label}"]`)
}

describe('QueriesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.queries.list.mockImplementation(async (params) => answers(params))
    api.queries.summary.mockResolvedValue({ total: 450, blocked: 0, clients: 1 })
    api.blocking.status.mockResolvedValue({ lists: [] })
  })

  // Reading back through a big log used to keep every page it had read,
  // so a million answers ended up in the browser. One page is kept.
  it('pages through the log a page at a time', async () => {
    const config = useConfigStore()
    config.draft = { version: 7, services: { dns: { enabled: true, queryLog: { enabled: true } } } }
    config.loaded = true
    const wrapper = mount(QueriesTab)
    await flushPromises()

    expect(names(wrapper)).toHaveLength(200)
    expect(names(wrapper)[0]).toBe('name450.example')
    expect(wrapper.text()).toContain('1–200 of 450.')
    expect(button(wrapper, 'Newer')).toBeUndefined()

    await button(wrapper, 'Older').trigger('click')
    await flushPromises()
    expect(api.queries.list).toHaveBeenLastCalledWith(expect.objectContaining({ before: 251 }))
    expect(names(wrapper)).toHaveLength(200)
    expect(names(wrapper)[0]).toBe('name250.example')
    expect(wrapper.text()).toContain('201–400 of 450.')

    await button(wrapper, 'Older').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toHaveLength(50)
    expect(wrapper.text()).toContain('401–450 of 450.')
    expect(button(wrapper, 'Older')).toBeUndefined()

    await button(wrapper, 'Newer').trigger('click')
    await flushPromises()
    expect(names(wrapper)[0]).toBe('name250.example')
    await button(wrapper, 'Newer').trigger('click')
    await flushPromises()
    expect(names(wrapper)[0]).toBe('name450.example')
    expect(button(wrapper, 'Newer')).toBeUndefined()
  })

  // Live reads the newest page again as it starts, so the stream picks up
  // where the page ends; a row in both shows once. Off, the rows stay.
  it('streams new answers on top while live', async () => {
    let source = null
    vi.stubGlobal(
      'EventSource',
      class {
        constructor() {
          source = this
          this.closed = false
        }
        close() {
          this.closed = true
        }
      },
    )
    const send = (seq) =>
      source.onmessage({
        data: JSON.stringify({
          seq,
          time: '2026-09-24T12:00:01Z',
          client: '10.0.0.2',
          name: `name${seq}.example`,
          type: 'A',
          status: 'ok',
        }),
      })
    const config = useConfigStore()
    config.draft = { version: 7, services: { dns: { enabled: true, queryLog: { enabled: true } } } }
    config.loaded = true
    const wrapper = mount(QueriesTab)
    await flushPromises()

    const live = button(wrapper, 'Live')
    await live.trigger('click')
    expect(live.attributes('aria-pressed')).toBe('true')
    send(450)
    await flushPromises()
    expect(api.queries.list).toHaveBeenCalledTimes(2)
    send(451)
    await flushPromises()
    expect(names(wrapper).slice(0, 3)).toEqual([
      'name451.example',
      'name450.example',
      'name449.example',
    ])

    await live.trigger('click')
    expect(source.closed).toBe(true)
    expect(names(wrapper)[0]).toBe('name451.example')

    // An older page has nothing new above it.
    await button(wrapper, 'Older').trigger('click')
    await flushPromises()
    expect(names(wrapper)[0]).toBe('name250.example')
    vi.unstubAllGlobals()
  })

  it('toggles a name onto the exception lists from its row', async () => {
    api.queries.list.mockResolvedValue(mixed())
    const config = saved()
    const wrapper = mount(QueriesTab)
    await flushPromises()

    expect(config.dirty).toBe(false)
    const allow = () => toggle(wrapper, 'Allow ads.example.com')
    const block = () => toggle(wrapper, 'Block example.com')
    expect(allow().element.checked).toBe(false)
    expect(block().element.checked).toBe(false)
    // The router answers for its own names, so they get none.
    expect(wrapper.find('input[aria-label$=" nas.lan"]').exists()).toBe(false)
    expect(toggle(wrapper, 'Block _dns.resolver.arpa').exists()).toBe(true)

    await allow().setValue(true)
    expect(config.draft.blocking.allow).toEqual(['ads.example.com'])
    expect(allow().element.checked).toBe(true)
    await block().setValue(true)
    expect(config.draft.blocking.deny).toEqual(['example.com'])
    expect(block().element.checked).toBe(true)

    // Put back, the draft is what was saved.
    await allow().setValue(false)
    await block().setValue(false)
    expect(allow().element.checked).toBe(false)
    expect(config.dirty).toBe(false)
  })

  it('offers a viewer no toggles', async () => {
    useAuthStore().user = { username: 'v', role: 'viewer' }
    api.queries.list.mockResolvedValue(mixed())
    saved()
    const wrapper = mount(QueriesTab)
    await flushPromises()
    expect(names(wrapper)).toHaveLength(4)
    expect(wrapper.findAll('tbody input')).toHaveLength(0)
  })
})
