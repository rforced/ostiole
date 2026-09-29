import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import PeerLogTab from '@/views/vpn/PeerLogTab.vue'

vi.mock('@/lib/api', () => ({
  api: {
    wireguard: { log: vi.fn() },
    tailscale: { log: vi.fn() },
    systemStats: vi.fn(),
    logFiles: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}))

class FakeSource {
  close() {}
}

async function tab(kind, list, { level = 'info', on = true } = {}) {
  api[kind].log.mockResolvedValue({ entries: list, held: list.length, kept: level === 'info' })
  api.systemStats.mockResolvedValue({ memTotal: 8_000_000_000 })
  api.logFiles.mockResolvedValue({ enabled: false, logs: [] })
  const store = useConfigStore()
  const draft = { version: 11, system: { logging: { level } }, interfaces: [] }
  if (on)
    draft.interfaces.push({ name: kind === 'wireguard' ? 'wg0' : 'ts0', enabled: true, [kind]: {} })
  store.draft = draft
  store.saved = structuredClone(draft)
  store.loaded = true
  const wrapper = mount(PeerLogTab, { props: { kind } })
  await flushPromises()
  return { wrapper, store }
}

describe('PeerLogTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it("shows a WireGuard tunnel's peers coming and going", async () => {
    const { wrapper } = await tab('wireguard', [
      { seq: 2, time: '2026-09-28T14:05:00Z', event: 'quiet', tunnel: 'wg0', peer: 'phone' },
      {
        seq: 1,
        time: '2026-09-28T14:02:00Z',
        event: 'connected',
        tunnel: 'wg0',
        peer: 'phone',
        endpoint: '203.0.113.5:51820',
      },
    ])
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].find('.badge').classes()).toContain('badge-warn')
    expect(rows[1].text()).toContain('wg0')
    expect(rows[1].text()).toContain('203.0.113.5:51820')
    expect(wrapper.text()).toContain('WireGuard log')
  })

  it('shows Tailscale peers without a tunnel column', async () => {
    const { wrapper } = await tab('tailscale', [
      { seq: 1, time: '2026-09-28T14:02:00Z', event: 'relayed', peer: 'laptop', endpoint: 'nyc' },
    ])
    expect(wrapper.findAll('th').map((th) => th.text())).toEqual(['Time', 'Event', 'Peer', 'Path'])
    expect(wrapper.find('td[data-label="Path"]').text()).toBe('nyc')
  })

  it('says which levels keep it, and when there is nothing to read', async () => {
    let { wrapper } = await tab('wireguard', [], { level: 'warning' })
    expect(wrapper.text()).toContain('The WireGuard log is kept at the Info and Debug log levels')
    ;({ wrapper } = await tab('tailscale', [], { on: false }))
    expect(wrapper.text()).toContain('Tailscale is off, so no peers arrive.')
  })

  it('keeps the vpn block out of the draft while it is empty', async () => {
    const { wrapper, store } = await tab('wireguard', [])
    await wrapper.find('#wireguard-days').setValue('3')
    expect(store.draft.vpn).toEqual({ wireguardLog: { days: 3 } })
    await wrapper.find('#wireguard-days').setValue('')
    expect(store.draft.vpn).toBeUndefined()
  })
})
