import { RouterLinkStub, flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import TrafficCard from '@/views/dashboard/TrafficCard.vue'

vi.mock('@/lib/api', () => ({ api: { traffic: { interfaces: vi.fn() } } }))

class FakeSource {
  static CLOSED = 2
  close() {}
}

const NOW = 1_790_000_000
const link = (name) => ({
  name,
  down: 1_000_000,
  up: 500_000,
  totals: { down: 0, up: 0 },
  points: [[NOW, 1_000_000, 500_000]],
})

function open(links, props = {}) {
  return mount(TrafficCard, {
    props: { links, ...props },
    global: { stubs: { RouterLink: RouterLinkStub } },
  })
}

describe('TrafficCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
    api.traffic.interfaces.mockResolvedValue({
      window: '5m',
      now: new Date(NOW * 1000).toISOString(),
      links: [link('eth0'), link('eth1')],
    })
  })

  // A chart for each external link, and nothing about devices.
  it('charts the external links', async () => {
    const w = open(['eth0'])
    await flushPromises()
    expect(w.findAll('svg[role="img"]')).toHaveLength(1)
    expect(w.get('svg[role="img"]').attributes('aria-label')).toBe(
      'eth0: down 1 Mbit/s, up 500 kbit/s',
    )
    expect(w.find('dl').exists()).toBe(false)
  })

  // Until the dashboard is read, a grey chart for each link the last
  // visit had, as the other cards draw their rows. The card was empty.
  it('draws a skeleton chart for each link the last visit had', async () => {
    const w = open([], { loaded: false, placeholders: 2 })
    await flushPromises()
    expect(w.get('section').attributes('aria-busy')).toBe('true')
    const held = w.findAll('[data-reading]')
    expect(held).toHaveLength(2)
    for (const h of held) {
      expect(h.attributes('aria-hidden')).toBe('true')
      expect(h.find('.animate-pulse path').exists()).toBe(true)
    }
    // Read already, and kept until the dashboard draws every card.
    expect(w.emitted('read')).toHaveLength(1)
    expect(w.find('svg[role="img"]').exists()).toBe(false)
    expect(w.text()).not.toContain('Mbit/s')
  })

  // The dashboard waits for this, even when the read fails.
  it('says when its first read has answered, with charts or without', async () => {
    api.traffic.interfaces.mockRejectedValue(new Error('traffic is not counted'))
    const w = open(['eth0'])
    expect(w.emitted('read')).toBeUndefined()
    await flushPromises()
    expect(w.emitted('read')).toHaveLength(1)
    expect(w.get('[role="alert"]').text()).toBe('traffic is not counted')
  })

  // The dashboard draws the card from the last visit's shape before the
  // overview names the links. The chart stayed empty until the stream
  // reopened.
  it('charts a link named after the card read them', async () => {
    const w = open([], { loaded: false })
    await flushPromises()
    await w.setProps({ links: ['eth0'], loaded: true })
    expect(w.find('[data-reading]').exists()).toBe(false)
    expect(w.get('section').attributes('aria-busy')).toBeUndefined()
    expect(w.get('svg[role="img"]').attributes('aria-label')).toBe(
      'eth0: down 1 Mbit/s, up 500 kbit/s',
    )
    expect(api.traffic.interfaces).toHaveBeenCalledTimes(1)
  })
})
