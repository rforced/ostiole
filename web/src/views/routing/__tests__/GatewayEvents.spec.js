import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import GatewayEvents from '@/views/routing/GatewayEvents.vue'

vi.mock('@/lib/api', () => ({
  api: { gatewayHistory: { events: vi.fn(), clearEvents: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

let source = null
class FakeSource {
  constructor() {
    source = this
  }
  close() {}
}

const EVENTS = [
  { seq: 3, time: '2026-10-08T14:10:00Z', gateway: 'gw_eth0', kind: 'up', for: 240 },
  { seq: 2, time: '2026-10-08T14:06:00Z', gateway: 'gw_eth0', kind: 'down', error: 'timeout' },
  { seq: 1, time: '2026-10-08T14:01:00Z', gateway: 'lte', family: 'IPv6', kind: 'family-never' },
]

async function card(files = false) {
  api.gatewayHistory.events.mockResolvedValue({ entries: EVENTS, held: 3, oldest: EVENTS[2].time })
  const store = useConfigStore()
  const draft = { version: 12, system: { logging: { files: { enabled: files } } } }
  store.draft = draft
  store.saved = structuredClone(draft)
  const wrapper = mount(GatewayEvents)
  await flushPromises()
  return wrapper
}

describe('GatewayEvents', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('lists what changed, newest first, with how long it lasted', async () => {
    const w = await card()
    const rows = w.findAll('tbody tr').map((r) => r.findAll('td').map((c) => c.text()))
    expect(rows[0].slice(1)).toEqual(['gw_eth0', 'Up again', '4m'])
    expect(rows[1].slice(1)).toEqual(['gw_eth0', 'Down timeout', '—'])
    expect(rows[2].slice(1)).toEqual(['lte', 'IPv6 never answered', '—'])
    expect(w.text()).toContain('3 entries back to')
    expect(w.text()).toContain('Kept in memory, so a restart empties it.')
    expect((await card(true)).text()).toContain('The files keep them through a restart.')
  })

  it('takes new events from the stream while Live is on', async () => {
    const w = await card()
    source.onmessage({
      data: JSON.stringify({ seq: 4, time: '2026-10-08T14:12:00Z', gateway: 'lte', kind: 'never' }),
    })
    await flushPromises()
    expect(w.findAll('tbody tr')[0].text()).toContain('Never answered')
  })

  it('clears the events, once asked', async () => {
    useAuthStore().user = { username: 'root', role: 'admin' }
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const w = await card()
    await w
      .findAll('button')
      .find((b) => b.text() === 'Clear')
      .trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenLastCalledWith(
      expect.objectContaining({ question: 'Clear the gateway events?' }),
    )
    expect(api.gatewayHistory.clearEvents).toHaveBeenCalledOnce()
  })
})
