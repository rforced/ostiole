import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import DevicesTab from '@/views/traffic/DevicesTab.vue'

vi.mock('@/lib/api', () => ({
  api: { traffic: { devices: vi.fn(), device: vi.fn(), clear: vi.fn() } },
}))

class FakeSource {
  static CLOSED = 2
  close() {}
}

const NOW = '2026-09-27T12:00:00Z'
const devices = [
  {
    id: 'a8:bb:cc:00:00:05',
    mac: 'a8:bb:cc:00:00:05',
    name: 'laptop',
    addresses: ['10.0.0.5'],
    interface: 'eth1',
    down: 8_000_000,
    up: 1_000_000,
    totals: { down: 3_000_000_000, up: 20_000_000 },
    lastSeen: NOW,
  },
  {
    id: '6a:00:00:00:00:07',
    mac: '6a:00:00:00:00:07',
    addresses: ['10.0.0.7'],
    interface: 'eth1',
    down: 0,
    up: 0,
    totals: { down: 1000, up: 1000 },
    lastSeen: NOW,
  },
  {
    id: 'router',
    router: true,
    addresses: ['203.0.113.2'],
    down: 1000,
    up: 2000,
    totals: { down: 5000, up: 6000 },
    lastSeen: NOW,
  },
]

async function open({
  role = 'admin',
  counting = true,
  draft = { devices: true },
  extra = {},
} = {}) {
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  const cfg = { version: 11, zones: [], interfaces: [], rules: [], system: {}, traffic: draft }
  if (!draft) delete cfg.traffic
  config.replaceDraft(cfg)
  config.markSaved()
  api.traffic.devices.mockResolvedValue({
    window: '5m',
    now: NOW,
    counting,
    interval: 5,
    since: '2026-09-27T09:00:00Z',
    devices: counting ? devices : [],
    ...extra,
  })
  const w = mount(DevicesTab, { global: { stubs: { DeviceDialog: true } } })
  await flushPromises()
  return { w, config }
}

describe('DevicesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('writes the switch into the draft, and takes it out again', async () => {
    const { w, config } = await open({ counting: false, draft: null })
    const box = w.get('input[type="checkbox"]')
    expect(box.element.checked).toBe(false)
    expect(w.text()).toContain('A restart clears it unless System → General writes logs to files.')
    await box.setValue(true)
    expect(config.draft.traffic).toEqual({ devices: true })
    expect(w.text()).toContain('Apply the draft to start it.')
    await box.setValue(false)
    expect(config.draft.traffic).toBeUndefined()
    expect(config.dirty).toBe(false)
  })

  // Destinations belong to devices: switching counting off takes them too.
  it('switches destinations off with the devices', async () => {
    const { w, config } = await open({
      draft: { devices: true, destinations: { enabled: true, days: 3 } },
    })
    await w.get('input[type="checkbox"]').setValue(false)
    expect(config.draft.traffic).toEqual({ destinations: { days: 3 } })
  })

  it('is locked for a viewer', async () => {
    const { w } = await open({ role: 'viewer' })
    expect(w.find('fieldset[disabled]').exists()).toBe(true)
    expect(w.findAll('button').some((b) => b.text() === 'Clear')).toBe(false)
  })

  // A device is its name, with its MAC under it, or This router.
  it('lists each device with its rates and totals', async () => {
    const { w } = await open()
    const rows = w.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[0].text()).toContain('laptop')
    expect(rows[0].text()).toContain('a8:bb:cc:00:00:05')
    expect(rows[0].text()).toContain('8 Mbit/s')
    expect(rows[0].text()).toContain('3.0 GB')
    expect(rows[1].text()).toContain('random')
    expect(rows[0].text()).not.toContain('random')
    expect(rows[2].text()).toContain('This router')
    await w.get('input[type="search"]').setValue('laptop')
    expect(w.findAll('tbody tr')).toHaveLength(1)
    await w.get('input[type="search"]').setValue('nothing here')
    expect(w.get('tbody').text()).toBe('Nothing matches "nothing here".')
  })

  it('says when a big table stretches the interval, and why devices are missing', async () => {
    const { w } = await open({
      extra: {
        interval: 20,
        error: 'Could not read the connection table: operation not permitted',
      },
    })
    expect(w.text()).toContain('Devices every 20 s')
    expect(w.get('[role="alert"]').text()).toContain('Could not read the connection table')
  })

  it('shows nothing of the devices while counting is off', async () => {
    const { w } = await open({ counting: false, draft: null })
    expect(w.text()).not.toContain('No devices.')
    expect(w.find('table').exists()).toBe(false)
  })
})
