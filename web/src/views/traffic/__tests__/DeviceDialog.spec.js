import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import DeviceDialog from '@/views/traffic/DeviceDialog.vue'

vi.mock('@/lib/api', () => ({ api: { traffic: { device: vi.fn(), destinations: vi.fn() } } }))

const stubs = {
  AppDialog: {
    props: ['open', 'title'],
    template: '<div v-if="open"><h2>{{ title }}</h2><slot /></div>',
  },
}

const NOW = 1_790_000_000
const device = {
  id: 'a8:bb:cc:00:00:05',
  mac: 'a8:bb:cc:00:00:05',
  name: 'laptop',
  addresses: ['10.0.0.5', '2001:db8::5'],
  interface: 'eth1',
  down: 0,
  up: 0,
  totals: { down: 0, up: 0 },
}

function open(props = {}) {
  return mount(DeviceDialog, {
    props: { open: true, device, window: '5m', ...props },
    global: { stubs },
  })
}

describe('DeviceDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.traffic.device.mockResolvedValue({
      ...device,
      down: 8_000_000,
      up: 1_000_000,
      totals: { down: 5_000_000, up: 1_000_000 },
      points: [[NOW, 8_000_000, 1_000_000]],
    })
  })

  // Its chart, how it is known, and its five busiest destinations over
  // the matching window while they are recorded.
  it('charts the device and names its top destinations', async () => {
    api.traffic.destinations.mockResolvedValue({
      enabled: true,
      entries: [
        {
          destination: 'example.com',
          protocol: 'tcp',
          port: 443,
          service: 'HTTPS',
          down: 4_000_000,
          up: 1,
        },
      ],
    })
    const w = open()
    await flushPromises()
    expect(api.traffic.device).toHaveBeenCalledWith('a8:bb:cc:00:00:05', '5m')
    expect(api.traffic.destinations).toHaveBeenCalledWith({
      device: 'a8:bb:cc:00:00:05',
      window: '1h',
      limit: 5,
    })
    expect(w.get('h2').text()).toBe('laptop')
    expect(w.get('svg[role="img"]').attributes('aria-label')).toBe(
      'laptop: down 8 Mbit/s, up 1 Mbit/s',
    )
    expect(w.text()).toContain('2001:db8::5')
    expect(w.text()).toContain('5.0 MB down')
    expect(w.text()).toContain('Top destinations')
    expect(w.text()).toContain('example.com')
    expect(w.text()).toContain('HTTPS')
  })

  // Over five minutes the chart takes each read of the table.
  it('takes a point from each devices event, and none for other devices', async () => {
    api.traffic.destinations.mockResolvedValue({ enabled: false, entries: [] })
    const w = open()
    await flushPromises()
    expect(w.text()).not.toContain('Top destinations')
    const event = (id, down) => ({
      kind: 'devices',
      time: new Date((NOW + 5) * 1000).toISOString(),
      devices: [{ id, down, up: 0 }],
    })
    await w.setProps({ latest: event('someone-else', 1) })
    expect(w.findAll('path')[0].attributes('d').split('L')).toHaveLength(1)
    await w.setProps({ latest: event('a8:bb:cc:00:00:05', 2_000_000) })
    expect(w.findAll('path')[0].attributes('d').split('L')).toHaveLength(2)
    expect(w.text()).toContain('Down 2 Mbit/s')
  })
})
