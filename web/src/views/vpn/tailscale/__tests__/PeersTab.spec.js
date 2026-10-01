import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import PeersTab from '@/views/vpn/tailscale/PeersTab.vue'

vi.mock('@/lib/api', () => ({
  api: { tailscale: { status: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

// Live reads from the start, so no tab outlives its test.
enableAutoUnmount(afterEach)

const peer = (hostName, over = {}) => ({
  hostName,
  dnsName: `${hostName}.tail.ts.net.`,
  ips: ['100.64.0.1'],
  os: 'linux',
  online: false,
  routes: [],
  ...over,
})

describe('PeersTab', () => {
  beforeEach(() => vi.clearAllMocks())

  // Online now counts as seen now, so the peers that are up come first.
  it('sorts by when a peer was last seen, the ones online first', async () => {
    api.tailscale.status.mockResolvedValue({
      running: true,
      peers: [
        peer('old', { lastSeen: '2026-09-20T12:00:00Z' }),
        peer('up', { online: true }),
        peer('never'),
        peer('recent', { lastSeen: '2026-09-25T12:00:00Z' }),
      ],
    })
    const wrapper = mount(PeersTab)
    await flushPromises()
    const nodes = () => wrapper.findAll('tbody tr').map((r) => r.find('.font-mono').text())
    expect(nodes()[0]).toBe('old.tail.ts.net')

    await wrapper
      .findAll('th')
      .find((th) => th.text() === 'Last seen')
      .get('button')
      .trigger('click')
    expect(nodes()).toEqual([
      'up.tail.ts.net',
      'recent.tail.ts.net',
      'old.tail.ts.net',
      'never.tail.ts.net',
    ])
  })

  // Peers come and go on the tailnet; off, the table holds still.
  it('reads again while Live is on, from the start', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    try {
      api.tailscale.status.mockResolvedValue({ running: true, peers: [peer('up')] })
      const wrapper = mount(PeersTab)
      await flushPromises()
      const live = wrapper.findAll('button').find((b) => b.text() === 'Live')
      expect(live.attributes('aria-pressed')).toBe('true')
      vi.advanceTimersByTime(5000)
      await flushPromises()
      expect(api.tailscale.status).toHaveBeenCalledTimes(2)
      await live.trigger('click')
      vi.advanceTimersByTime(15000)
      expect(api.tailscale.status).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })
})
