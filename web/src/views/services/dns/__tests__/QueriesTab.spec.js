import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import QueriesTab from '@/views/services/dns/QueriesTab.vue'

vi.mock('@/lib/api', () => ({
  api: {
    queries: { list: vi.fn(), summary: vi.fn(), clear: vi.fn() },
    blocking: { status: vi.fn(), lookup: vi.fn() },
    systemStats: vi.fn(() => Promise.resolve({ memTotal: 0 })),
    logFiles: vi.fn(() => Promise.resolve({ enabled: false, logs: [] })),
    logLimits: vi.fn(() => Promise.resolve({ memTotal: 0, ceilings: {} })),
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
  const entries = all.slice(from, from + 200)
  const last = entries[entries.length - 1]
  const more = last.seq > 1
  return { enabled: true, held: all.length, entries, more, next: more ? last.seq : undefined }
}

/** The stream the tab opens; a test sends through the last one. */
let source = null
class FakeSource {
  constructor() {
    source = this
    this.closed = false
  }
  close() {
    this.closed = true
  }
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
    held: 4,
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
    version: 11,
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
    vi.stubGlobal('EventSource', FakeSource)
    api.queries.list.mockImplementation(async (params) => answers(params))
    api.queries.summary.mockResolvedValue({ total: 450, blocked: 0, clients: 1 })
    api.blocking.status.mockResolvedValue({ lists: [] })
  })

  // The entries sit beside the switch, and off hides them but keeps them
  // for when it is on again.
  it('keeps the entries beside the switch', async () => {
    const config = saved()
    const wrapper = mount(QueriesTab)
    await flushPromises()
    expect(wrapper.text()).toContain('100,000 is the default. In memory, about 15.0 MB when full.')
    await wrapper.get('#queries-entries').setValue('30000')
    expect(config.draft.services.dns.queryLog).toEqual({ enabled: true, entries: 30000 })
    await toggle(wrapper, 'Query log enabled').setValue(false)
    expect(config.draft.services.dns.queryLog).toEqual({ entries: 30000 })
    expect(wrapper.find('#queries-entries').exists()).toBe(false)
    await toggle(wrapper, 'Query log enabled').setValue(true)
    await wrapper.get('#queries-entries').setValue('')
    expect(config.dirty).toBe(false)
  })

  // A restart clears the log unless the logs go to files as well, and then
  // Clear takes the files too.
  it('says what a restart and Clear take while the logs go to files', async () => {
    saved()
    let wrapper = mount(QueriesTab)
    await flushPromises()
    expect(wrapper.text()).toContain('Switching it off, or a restart, clears it.')
    setActivePinia(createPinia())
    saved().draft.system = { logging: { files: { enabled: true } } }
    wrapper = mount(QueriesTab)
    await flushPromises()
    expect(wrapper.text()).toContain(
      'Kept on this router only. Switching it off clears it, files included.',
    )
  })

  // Older answers are read from the table's foot, a page at a time, and
  // reading them turns Live off.
  it('reads older answers from its foot', async () => {
    saved()
    const wrapper = mount(QueriesTab)
    await flushPromises()
    expect(names(wrapper)).toHaveLength(200)
    expect(names(wrapper)[0]).toBe('name450.example')
    expect(wrapper.text()).toContain('450 entries.')

    await button(wrapper, 'Load more').trigger('click')
    await flushPromises()
    expect(api.queries.list).toHaveBeenLastCalledWith(
      { limit: 200, before: 251 },
      expect.any(AbortSignal),
    )
    expect(names(wrapper)).toHaveLength(400)
    expect(button(wrapper, 'Live').attributes('aria-pressed')).toBe('false')
    await button(wrapper, 'Load more').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toHaveLength(450)
    expect(button(wrapper, 'Load more')).toBeUndefined()
    expect(wrapper.text()).toContain('Start of the log.')
  })

  // Live is on from the start. Off, what arrives waits; on, it lands on top.
  it('streams new answers on top while live', async () => {
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
    saved()
    const wrapper = mount(QueriesTab)
    await flushPromises()

    const live = button(wrapper, 'Live')
    expect(live.attributes('aria-pressed')).toBe('true')
    send(450)
    send(451)
    await flushPromises()
    expect(names(wrapper).slice(0, 3)).toEqual([
      'name451.example',
      'name450.example',
      'name449.example',
    ])

    await live.trigger('click')
    send(452)
    await flushPromises()
    expect(names(wrapper)[0]).toBe('name451.example')
    await live.trigger('click')
    expect(names(wrapper)[0]).toBe('name452.example')
  })

  // The selects narrow on the router; the search goes to it once typing
  // rests.
  it('sends the selects and the search to the router', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      saved()
      const wrapper = mount(QueriesTab)
      await flushPromises()
      await wrapper.get('select[aria-label="Status"]').setValue('blocked')
      await flushPromises()
      expect(api.queries.list).toHaveBeenLastCalledWith(
        { status: 'blocked', limit: 200 },
        expect.any(AbortSignal),
      )
      await wrapper.get('input[type=search]').setValue('laptop')
      vi.advanceTimersByTime(250)
      await flushPromises()
      expect(api.queries.list).toHaveBeenLastCalledWith(
        { status: 'blocked', limit: 200, q: 'laptop' },
        expect.any(AbortSignal),
      )
    } finally {
      vi.useRealTimers()
    }
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

  // Clear empties the router's log and its counts per list, and reads the
  // log again. Only an admin clears.
  it('clears the router’s log, once asked', async () => {
    useAuthStore().user = { username: 'root', role: 'admin' }
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    saved()
    const wrapper = mount(QueriesTab)
    await flushPromises()
    const reads = api.queries.list.mock.calls.length
    await button(wrapper, 'Clear').trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({
        question: 'Clear the query log?',
        description: 'Every answer it holds, and the counts per list, are dropped.',
      }),
    )
    expect(api.queries.clear).toHaveBeenCalledOnce()
    expect(api.queries.list.mock.calls.length).toBeGreaterThan(reads)
  })
})
