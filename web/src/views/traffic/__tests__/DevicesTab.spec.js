import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
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

  // The card's switch, as every switch that governs a card is.
  it('writes the switch into the draft, and takes it out again', async () => {
    const { w, config } = await open({ counting: false, draft: null })
    const toggle = w.get('#devices-enabled')
    expect(toggle.attributes('role')).toBe('switch')
    expect(toggle.attributes('aria-label')).toBe('Counting enabled')
    expect(toggle.element.checked).toBe(false)
    expect(w.text()).toContain(
      'Kept in memory on this router only. Switching it off, or a restart, clears it.',
    )
    expect(w.text()).not.toContain('Apply the draft to start it.')
    await toggle.setValue(true)
    expect(config.draft.traffic).toEqual({ devices: true })
    expect(w.text()).toContain('Apply the draft to start it.')
    await toggle.setValue(false)
    expect(config.draft.traffic).toBeUndefined()
    expect(config.dirty).toBe(false)
  })

  // Applied, the draft and the router agree, whether or not the router
  // has started counting yet.
  it('asks for an apply only while the router runs without it', async () => {
    const { w } = await open({ counting: false })
    expect(w.text()).not.toContain('Apply the draft to start it.')
  })

  it('says what the files keep, as the draft has them', async () => {
    const { w, config } = await open()
    config.draft.system = { logging: { files: { enabled: true } } }
    await flushPromises()
    expect(w.text()).toContain(
      'Kept on this router only. Switching it off clears it, files included.',
    )
  })

  // Destinations belong to devices: switching counting off takes them too.
  it('switches destinations off with the devices', async () => {
    const { w, config } = await open({
      draft: { devices: true, destinations: { enabled: true, entries: 50000 } },
    })
    expect(w.text()).toContain('clears it. Destinations switch off with it.')
    await w.get('#devices-enabled').setValue(false)
    expect(config.draft.traffic).toEqual({ destinations: { entries: 50000 } })
    expect(w.text()).not.toContain('Destinations switch off with it.')
  })

  it('is locked for a viewer', async () => {
    const { w } = await open({ role: 'viewer' })
    expect(w.get('#devices-enabled').attributes('disabled')).toBeDefined()
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

  // Down and Up sort by the rate first, then by the window's total, and
  // the header says which.
  it('sorts Down and Up by the rate, then by the total', async () => {
    const quiet = { ...devices[0], down: 1000, up: 1000 }
    const busy = { ...devices[1], down: 5000, up: 5000 }
    const { w } = await open({ extra: { devices: [quiet, busy] } })
    const first = () => w.get('tbody tr').text()
    const header = (i) => w.findAll('th')[i]
    const reads = (th) =>
      th
        .findAll('button > span > span')
        .filter((s) => !s.classes('invisible'))
        .map((s) => s.text())
    for (const [i, name] of [
      [1, 'Down'],
      [2, 'Up'],
    ]) {
      await header(i).get('button').trigger('click')
      expect(reads(header(i))).toEqual([name])
      expect(header(i).attributes('aria-sort')).toBe('descending')
      expect(first()).toContain('6a:00:00:00:00:07')
      await header(i).get('button').trigger('click')
      expect(reads(header(i))).toEqual([`${name} total`])
      expect(header(i).attributes('aria-sort')).toBe('descending')
      expect(first()).toContain('laptop')
    }
    // The phone has no headers: each figure is its own choice.
    expect(
      w
        .get('select[aria-label="Sort"]')
        .findAll('option')
        .map((o) => o.text()),
    ).toEqual(['Device', 'Down', 'Down total', 'Up', 'Up total', 'Last seen'])
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

  // Clear forgets the router's devices and destinations, files included,
  // and reads again. Only an admin clears.
  it('clears the counts, once asked', async () => {
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const { w, config } = await open()
    const clear = () => w.findAll('button').find((b) => b.text() === 'Clear')
    const reads = api.traffic.devices.mock.calls.length
    await clear().trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenLastCalledWith(
      expect.objectContaining({
        question: 'Clear the traffic counts?',
        description:
          'Every device and what it moved is forgotten, and every destination. Counting carries on.',
      }),
    )
    expect(api.traffic.clear).toHaveBeenCalledOnce()
    expect(api.traffic.devices.mock.calls.length).toBeGreaterThan(reads)
    config.saved = { ...config.saved, system: { logging: { files: { enabled: true } } } }
    await flushPromises()
    await clear().trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenLastCalledWith(
      expect.objectContaining({
        description:
          'Every device and what it moved is forgotten, and every destination, files included. Counting carries on.',
      }),
    )
  })

  it('greys Clear for an operator', async () => {
    const { w } = await open({ role: 'operator' })
    const clear = w.findAll('button').find((b) => b.text() === 'Clear')
    expect(clear.attributes('disabled')).toBeDefined()
    expect(clear.attributes('title')).toBe('Only an admin can clear it.')
  })
})
