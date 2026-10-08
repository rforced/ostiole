import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import RequestsTab from '@/views/services/proxy/RequestsTab.vue'

vi.mock('@/lib/api', () => ({
  api: {
    proxy: { status: vi.fn(), requests: vi.fn(), clearRequests: vi.fn() },
    systemStats: vi.fn(),
    logFiles: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}))

class FakeSource {
  close() {}
}

const clearButton = (w) => w.findAll('button').find((b) => b.text() === 'Clear')

function request(over = {}) {
  return {
    seq: 1,
    time: '2026-09-28T14:02:00Z',
    site: 'vault',
    client: '203.0.113.9',
    method: 'GET',
    host: 'vault.example.com',
    path: '/notifications/hub',
    status: 101,
    bytes: 512,
    duration: 0.0123,
    agent: 'Bitwarden_Mobile/2026.9',
    ...over,
  }
}

async function tab(list, level = 'info') {
  api.proxy.requests.mockResolvedValue({ entries: list, held: list.length, kept: level === 'info' })
  api.proxy.status.mockResolvedValue({ setUp: true, running: true, ports: {}, upstreams: [] })
  api.systemStats.mockResolvedValue({ memTotal: 8_000_000_000 })
  api.logFiles.mockResolvedValue({ enabled: false, logs: [] })
  const store = useConfigStore()
  const draft = {
    version: 11,
    system: { logging: { level } },
    services: { proxy: { enabled: true, sites: [{ id: 'vault', enabled: true }] } },
  }
  store.draft = draft
  store.saved = structuredClone(draft)
  store.loaded = true
  const wrapper = mount(RequestsTab, { global: { stubs: { RouterLink: true } } })
  await flushPromises()
  return { wrapper, store }
}

describe('RequestsTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('shows what the proxy answered', async () => {
    const { wrapper } = await tab([request({ seq: 2, status: 200, duration: 1.5 }), request()])
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[1].text()).toContain('vault')
    expect(rows[1].text()).toContain('GET vault.example.com/notifications/hub')
    expect(rows[1].text()).toContain('101')
    expect(rows[1].text()).toContain('512 B · 12 ms')
    expect(rows[1].text()).toContain('Bitwarden_Mobile/2026.9')
    expect(rows[0].text()).toContain('1.5 s')
    expect(api.proxy.requests).toHaveBeenCalledWith({ limit: 200 }, expect.any(AbortSignal))
  })

  // Below Info the router keeps no line per request, and the tab says
  // where that is set.
  it('marks the answers the WAF or the proxy gave', async () => {
    const { wrapper } = await tab([
      request({ seq: 3, status: 403, by: 'waf' }),
      request({ seq: 2, status: 403, by: 'proxy', site: undefined }),
      request({ status: 403, by: 'site' }),
    ])
    const badges = wrapper.findAll('tbody tr').map((r) => r.findAll('.badge').map((b) => b.text()))
    expect(badges).toEqual([['WAF'], ['proxy'], []])
    expect(wrapper.find('tbody .badge').attributes('title')).toBe('The WAF refused it.')
  })

  it('says which log levels keep requests', async () => {
    let { wrapper } = await tab([], 'warning')
    expect(wrapper.text()).toContain('Requests are kept at the Info and Debug log levels')
    expect(wrapper.text()).toContain('No requests.')
    ;({ wrapper } = await tab([request()]))
    expect(wrapper.text()).not.toContain('Requests are kept at')
  })

  it('keeps how many requests and days in the draft', async () => {
    const { wrapper, store } = await tab([])
    await wrapper.find('#requests-entries').setValue('100000')
    await wrapper.find('#requests-days').setValue('3')
    expect(store.draft.services.proxy.requests).toEqual({ entries: 100000, days: 3 })
    expect(wrapper.text()).toContain('About 40.0 MB of memory when full.')
  })

  // Clear empties the router's log and reads it again. Below Info there is
  // nothing kept to clear.
  it('clears the router’s log, once asked', async () => {
    useAuthStore().user = { username: 'root', role: 'admin' }
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const { wrapper } = await tab([request()])
    api.proxy.requests.mockResolvedValue({ entries: [], held: 0, kept: true })
    await clearButton(wrapper).trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({
        question: 'Clear the proxy requests?',
        description: 'Every request it holds is dropped. The journal keeps its own copy.',
      }),
    )
    expect(api.proxy.clearRequests).toHaveBeenCalled()
    expect(wrapper.text()).toContain('No requests.')
    const { wrapper: warning } = await tab([], 'warning')
    expect(clearButton(warning)).toBeUndefined()
  })
})
