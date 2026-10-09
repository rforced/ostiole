import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import GatewayHistoryDialog from '@/views/routing/GatewayHistoryDialog.vue'

vi.mock('@/lib/api', () => ({
  api: { gatewayHistory: { read: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

let source = null
class FakeSource {
  constructor(url) {
    this.url = url
    source = this
  }
  close() {
    if (source === this) source = null
  }
}

const stubs = {
  AppDialog: {
    props: ['open', 'title'],
    template: '<div><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
  },
}

const NOW = 1_790_000_000
const at = (t) => new Date(t * 1000).toISOString()

function answer(window) {
  if (window === '5m') {
    return {
      window,
      now: at(NOW),
      families: [
        {
          family: 'IPv4',
          points: [
            [NOW - 10, 4, 4, 4, 0],
            [NOW - 5, null, null, null, 100],
          ],
          mean: 4,
          worst: 4,
          loss: 50,
          sent: 2,
        },
      ],
      down: 0,
      never: 0,
      events: [],
    }
  }
  return {
    window,
    now: at(NOW),
    families: [
      {
        family: 'IPv4',
        points: [[NOW - 120, 3.5, 3, 4, 0]],
        mean: 3.5,
        worst: 3.5,
        loss: 0,
        sent: 12,
      },
      {
        family: 'IPv6',
        points: [[NOW - 120, 6, 5, 7, 25]],
        mean: 6,
        worst: 6,
        loss: 25,
        sent: 12,
      },
    ],
    down: 240,
    never: 0,
    events: [{ seq: 2, time: at(NOW - 300), gateway: 'gw_eth0', kind: 'up', for: 240 }],
  }
}

function dialog(gateway = { name: 'gw_eth0', live: { families: [{}, {}] } }) {
  return mount(GatewayHistoryDialog, { props: { open: true, gateway }, global: { stubs } })
}

describe('GatewayHistoryDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
    api.gatewayHistory.read.mockImplementation(async (_, w) => answer(w))
  })

  it("charts a day of both families, with the day's figures and events", async () => {
    const w = dialog()
    await flushPromises()
    expect(api.gatewayHistory.read).toHaveBeenCalledWith('gw_eth0', '24h')
    expect(w.text()).toContain('Measured to both next hops.')
    const legend = w.text()
    expect(legend).toContain('IPv4 3.5 ms')
    expect(legend).toContain('IPv6 lost 25%')
    const kv = w.get('dl').text()
    expect(kv).toContain('3.5 ms mean, 3.5 ms worst, 0% lost')
    expect(kv).toContain('6.0 ms mean, 6.0 ms worst, 25% lost')
    expect(kv).toContain('Down in 24 hours4m')
    expect(w.text()).toContain('Events in 24 hours')
    expect(w.text()).toContain('Up again')
    expect(source).toBeNull()
  })

  it('takes each probe from the stream over five minutes', async () => {
    const w = dialog({ name: 'gw_eth0', monitor: '192.0.2.53' })
    await flushPromises()
    await w.get('select').setValue('5m')
    await flushPromises()
    expect(api.gatewayHistory.read).toHaveBeenLastCalledWith('gw_eth0', '5m')
    expect(w.text()).toContain('Measured to 192.0.2.53.')
    expect(source.url).toBe('/api/v1/gateways/stream')
    expect(w.get('dl').text()).toContain('4.0 ms mean, 4.0 ms worst, 50% lost')
    source.onmessage({
      data: JSON.stringify({ gateway: 'gw_eth0', family: 'IPv4', time: at(NOW), latencyMs: 8 }),
    })
    source.onmessage({
      data: JSON.stringify({ gateway: 'lte', family: 'IPv4', time: at(NOW), latencyMs: 90 }),
    })
    await flushPromises()
    expect(w.get('dl').text()).toContain('6.0 ms mean, 8.0 ms worst, 33% lost')
    await w.get('select').setValue('30d')
    await flushPromises()
    expect(source).toBeNull()
  })
})
