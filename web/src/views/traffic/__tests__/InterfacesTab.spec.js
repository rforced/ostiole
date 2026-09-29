import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import InterfacesTab from '@/views/traffic/InterfacesTab.vue'

vi.mock('@/lib/api', () => ({ api: { traffic: { interfaces: vi.fn() } } }))

let source = null
class FakeSource {
  static CLOSED = 2
  constructor() {
    source = this
  }
  close() {}
}
const second = (t, down, up) =>
  source.onmessage({
    data: JSON.stringify({
      kind: 'links',
      time: new Date(t * 1000).toISOString(),
      links: [{ id: 'eth0', down, up }],
    }),
  })

const NOW = 1_790_000_000
function answer(window, points = [[NOW - 1, 1000, 2000]]) {
  return {
    window,
    now: new Date(NOW * 1000).toISOString(),
    links: [
      {
        name: 'eth0',
        configured: true,
        external: true,
        description: 'Fibre',
        down: 1000,
        up: 2000,
        totals: { down: 5_000_000, up: 1_000_000 },
        since: new Date((NOW - 3600) * 1000).toISOString(),
        points,
      },
    ],
  }
}

describe('InterfacesTab', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
    api.traffic.interfaces.mockImplementation(async (w) => answer(w))
  })

  it('charts each link with its totals, and takes each second from the stream', async () => {
    const w = mount(InterfacesTab)
    await flushPromises()
    expect(api.traffic.interfaces).toHaveBeenCalledWith('5m')
    expect(w.text()).toContain('eth0')
    expect(w.text()).toContain('WAN')
    expect(w.text()).toContain('Fibre')
    expect(w.text()).toContain('5.0 MB')
    second(NOW + 1, 8_000_000, 1_000_000)
    await flushPromises()
    expect(w.text()).toContain('Down 8 Mbit/s')
    expect(w.findAll('path')[0].attributes('d').split('L')).toHaveLength(2)
  })

  // Off, the chart holds and the seconds wait; on again, they are drawn.
  it('holds the seconds while Live is off', async () => {
    const w = mount(InterfacesTab)
    await flushPromises()
    const live = w.findAll('button').find((b) => b.text() === 'Live')
    await live.trigger('click')
    second(NOW + 1, 8_000_000, 1_000_000)
    await flushPromises()
    expect(w.text()).not.toContain('Down 8 Mbit/s')
    await live.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('Down 8 Mbit/s')
  })

  // A new window keeps the old frame, dimmed, until its points arrive.
  it('keeps the old frame while another window is read', async () => {
    const w = mount(InterfacesTab)
    await flushPromises()
    let release
    api.traffic.interfaces.mockImplementation(
      (win) => new Promise((resolve) => (release = () => resolve(answer(win)))),
    )
    await w.get('select').setValue('24h')
    expect(api.traffic.interfaces).toHaveBeenLastCalledWith('24h')
    expect(w.get('svg[role="img"]').classes()).toContain('opacity-60')
    release()
    await flushPromises()
    expect(w.get('svg[role="img"]').classes()).not.toContain('opacity-60')
    // Counting began an hour ago, inside a day.
    expect(w.text()).toMatch(/Since .+\./)
  })
})
