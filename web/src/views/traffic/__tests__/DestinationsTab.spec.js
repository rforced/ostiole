import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { SETTLE_MS } from '@/lib/log'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import DestinationsTab from '@/views/traffic/DestinationsTab.vue'

vi.mock('@/lib/api', () => ({
  api: {
    traffic: { destinations: vi.fn(), devices: vi.fn(), clearDestinations: vi.fn() },
    systemStats: vi.fn(() => Promise.resolve({ memTotal: 0 })),
    logFiles: vi.fn(() => Promise.resolve({ enabled: false, logs: [] })),
  },
}))

class FakeSource {
  static CLOSED = 2
  close() {}
}

const rows = [
  {
    device: 'a8:bb:cc:00:00:05',
    deviceName: 'laptop',
    destination: 'example.com',
    address: '93.184.215.14',
    protocol: 'tcp',
    port: 443,
    service: 'HTTPS',
    down: 3_000_000,
    up: 20_000,
    connections: 12,
    lastSeen: '2026-09-27T12:00:00Z',
  },
  {
    device: 'router',
    destination: '9.9.9.9',
    address: '9.9.9.9',
    protocol: 'udp',
    port: 5353,
    down: 100,
    up: 100,
    connections: 1,
    lastSeen: '2026-09-27T12:00:00Z',
  },
]

async function open({
  traffic = { devices: true, destinations: { enabled: true } },
  enabled = true,
  role = 'admin',
} = {}) {
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  config.replaceDraft({
    version: 11,
    zones: [],
    interfaces: [],
    rules: [],
    system: {},
    ...(traffic ? { traffic } : {}),
  })
  config.markSaved()
  api.traffic.destinations.mockResolvedValue({
    window: '24h',
    enabled,
    entries: enabled ? rows : [],
    more: false,
    held: 40,
    oldest: '2026-09-27T09:00:00Z',
  })
  api.traffic.devices.mockResolvedValue({ devices: [{ id: 'a8:bb:cc:00:00:05', name: 'laptop' }] })
  const w = mount(DestinationsTab)
  await flushPromises()
  return { w, config }
}

describe('DestinationsTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })
  afterEach(() => vi.useRealTimers())

  // The switch needs counting per device, and the card says so while it
  // is off.
  it('offers the switch only with devices counted', async () => {
    let { w } = await open({ traffic: null, enabled: false })
    const toggle = w.get('#destinations-enabled')
    expect(toggle.attributes('role')).toBe('switch')
    expect(toggle.attributes('aria-label')).toBe('Recording enabled')
    expect(toggle.attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('Needs counting on the Devices tab.')
    expect(w.text()).not.toContain('Off.')
    let config
    ;({ w, config } = await open({ traffic: { devices: true }, enabled: false }))
    expect(w.text()).toContain('Off.')
    expect(w.text()).toContain(
      'Kept in memory on this router only. Switching it off, or a restart, clears it.',
    )
    expect(w.text()).not.toContain('Apply the draft to start it.')
    await w.get('#destinations-enabled').setValue(true)
    expect(config.draft.traffic).toEqual({ devices: true, destinations: { enabled: true } })
    expect(w.text()).toContain('Apply the draft to start it.')
    await w.get('#destinations-days').setValue('3')
    expect(config.draft.traffic.destinations).toEqual({ enabled: true, days: 3 })
    await w.get('#destinations-days').setValue('')
    await w.get('#destinations-enabled').setValue(false)
    expect(config.dirty).toBe(false)
  })

  it('ranks what each device talked to, with its service and counts', async () => {
    const { w } = await open()
    const body = w.findAll('tbody tr')
    expect(body).toHaveLength(2)
    expect(body[0].text()).toContain('example.com')
    expect(body[0].text()).toContain('93.184.215.14')
    expect(body[0].text()).toContain('laptop')
    expect(body[0].text()).toContain('HTTPS')
    expect(body[0].text()).toContain('3.0 MB')
    expect(body[0].text()).toContain('12')
    expect(body[1].text()).toContain('This router')
    expect(body[1].text()).toContain('5353/udp')
    expect(w.text()).toContain('40 entries back to')
  })

  // The window, the device and the words are asked of the router.
  it('asks the router for the window, the device and a search', async () => {
    const { w } = await open()
    expect(api.traffic.destinations).toHaveBeenLastCalledWith({ window: '24h' })
    await w.get('select[aria-label="Window"]').setValue('7d')
    await w.get('select[aria-label="Device"]').setValue('a8:bb:cc:00:00:05')
    await flushPromises()
    expect(api.traffic.destinations).toHaveBeenLastCalledWith({
      window: '7d',
      device: 'a8:bb:cc:00:00:05',
    })
    // With one device chosen, its column goes.
    expect(w.get('thead').text()).not.toContain('Device')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await w.get('input[type="search"]').setValue('example')
    vi.advanceTimersByTime(SETTLE_MS)
    await flushPromises()
    expect(api.traffic.destinations).toHaveBeenLastCalledWith({
      window: '7d',
      device: 'a8:bb:cc:00:00:05',
      q: 'example',
    })
  })

  it('shows no table while destinations are not recorded', async () => {
    const { w } = await open({ traffic: { devices: true }, enabled: false })
    expect(w.find('table').exists()).toBe(false)
  })

  // Clear forgets the router's destinations, files included, and reads
  // them again. The devices are the Devices tab's to clear.
  it('clears the destinations, once asked', async () => {
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const { w } = await open()
    const reads = api.traffic.destinations.mock.calls.length
    await w
      .findAll('button')
      .find((b) => b.text() === 'Clear')
      .trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({
        question: 'Clear the destinations?',
        description: 'Every destination it holds is dropped.',
      }),
    )
    expect(api.traffic.clearDestinations).toHaveBeenCalledOnce()
    expect(api.traffic.destinations.mock.calls.length).toBeGreaterThan(reads)
  })
})
