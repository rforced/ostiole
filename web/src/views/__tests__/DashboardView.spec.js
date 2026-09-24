import { RouterLinkStub, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import DashboardView from '@/views/DashboardView.vue'
import { SHAPE_KEY } from '@/views/dashboard/shape'

vi.mock('@/lib/api', () => ({
  api: {
    health: vi.fn(),
    overview: vi.fn(),
    status: vi.fn(),
    systemStats: vi.fn(),
    update: { status: vi.fn() },
  },
}))

const stats = {
  cpuPercent: 12.4,
  cores: 2,
  threads: 2,
  load1: 0.1,
  load5: 0.05,
  load15: 0.01,
  memTotal: 4_000_000_000,
  memAvailable: 3_000_000_000,
  uptimeSeconds: 90_000,
  filesystems: [{ path: '/', total: 20_000_000_000, free: 15_000_000_000 }],
}

const overview = {
  status: { hostname: 'fw', rules: 3, zones: 2, revisions: 1 },
  interfaces: [],
  topRules: [],
  blocked: { packets: 9, bytes: 540 },
  services: [],
  warnings: [],
  gateways: [],
  unwatchedGateways: [],
  recentLeases: [],
  dhcp: {},
  dns: {},
}

/** The card headed by title. */
const card = (w, title) =>
  w.findAll('section').find((s) => s.find('h2').exists() && s.find('h2').text() === title)
/** Every card's title, in page order. */
const titles = (w) => w.findAll('section h2').map((h) => h.text())
/** A card's placeholder rows. */
const reading = (w, title) => card(w, title).findAll('tr[data-reading]')

describe('DashboardView', () => {
  /** @type {(value: object) => void} */
  let answer

  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    // The polls.
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    api.health.mockResolvedValue({ status: 'ok', version: 'v1.0.0' })
    api.status.mockResolvedValue({ configured: true, tableLoaded: true, network: 'networkd' })
    api.systemStats.mockResolvedValue(stats)
    api.update.status.mockResolvedValue({})
    api.overview.mockReturnValue(new Promise((resolve) => (answer = resolve)))
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  /** The header reads the route, and there is none here. */
  async function mountDashboard() {
    const w = mount(DashboardView, {
      global: { stubs: { RouterLink: RouterLinkStub, PageHeader: true } },
    })
    await flushPromises()
    return w
  }

  it('fills the usage meters while the overview is still being read', async () => {
    const w = await mountDashboard()
    const system = card(w, 'System')
    expect(system.attributes('aria-busy')).toBeUndefined()
    expect(system.text()).toContain('12%')

    for (const title of ['Interfaces', 'Busiest rules', 'Services', 'Router']) {
      expect(card(w, title).attributes('aria-busy')).toBe('true')
      expect(card(w, title).text()).toContain('Reading…')
    }
    expect(card(w, 'Busiest rules').text()).not.toContain('0 packets')
    expect(card(w, 'Router').text()).not.toContain('0 rules')
  })

  it('holds room for the cards every router has on a first visit', async () => {
    const w = await mountDashboard()
    expect(titles(w)).toEqual(['Interfaces', 'System', 'Busiest rules', 'Services', 'Router'])
    expect(reading(w, 'Interfaces')).toHaveLength(3)
    expect(reading(w, 'Busiest rules')).toHaveLength(3)
  })

  it('holds room in the shape the dashboard had last time', async () => {
    localStorage.setItem(
      SHAPE_KEY,
      JSON.stringify({
        warnings: 2,
        interfaces: 4,
        rules: 1,
        services: 2,
        cards: { gateways: 2, leases: 0 },
      }),
    )
    const w = await mountDashboard()
    expect(titles(w)).toEqual([
      'Interfaces',
      'System',
      'Gateways',
      'Busiest rules',
      'Services',
      'Newest leases',
      'Router',
    ])
    expect(w.findAll('[data-reading] > .rounded-lg')).toHaveLength(2)
    expect(reading(w, 'Interfaces')).toHaveLength(4)
    expect(reading(w, 'Gateways')).toHaveLength(2)
    expect(reading(w, 'Busiest rules')).toHaveLength(1)
    // No leases last time: one line, as tall as the one that says so.
    expect(reading(w, 'Newest leases')).toHaveLength(1)
    expect(reading(w, 'Newest leases')[0].find('td').attributes('colspan')).toBe('3')
  })

  it('fills the cards in where they stand, and remembers their shape', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const w = await mountDashboard()
    answer({
      ...overview,
      gateways: [{ name: 'wan', interface: 'eth0', online: true, latencyMs: 1, lossPercent: 0 }],
    })
    await flushPromises()

    expect(warn).not.toHaveBeenCalled()
    expect(w.find('[data-reading]').exists()).toBe(false)
    expect(titles(w)).toEqual([
      'Interfaces',
      'System',
      'Gateways',
      'Busiest rules',
      'Services',
      'Router',
    ])
    for (const section of w.findAll('section')) {
      expect(section.attributes('aria-busy')).toBeUndefined()
    }
    expect(card(w, 'Gateways').text()).toContain('wan')
    expect(card(w, 'Interfaces').text()).toContain('No interfaces.')
    expect(card(w, 'Busiest rules').text()).toContain('9 packets')
    expect(card(w, 'Router').text()).toContain('3 rules in 2 zones')
    expect(JSON.parse(localStorage.getItem(SHAPE_KEY))).toEqual({
      warnings: 0,
      interfaces: 0,
      rules: 0,
      services: 0,
      cards: { gateways: 1 },
      // jsdom lays nothing out, so there is no height to keep.
      heights: {},
    })
  })

  it('holds the room each card took last time, until it is read', async () => {
    localStorage.setItem(
      SHAPE_KEY,
      JSON.stringify({ cards: { gateways: 1 }, heights: { interfaces: 290, gateways: 334 } }),
    )
    const w = await mountDashboard()
    const held = (title) => card(w, title).element.style.minHeight
    expect(held('Interfaces')).toBe('290px')
    expect(held('Gateways')).toBe('334px')
    expect(held('Router')).toBe('')
    answer({
      ...overview,
      gateways: [{ name: 'wan', interface: 'eth0', online: true, latencyMs: 1, lossPercent: 0 }],
    })
    await flushPromises()
    expect(held('Interfaces')).toBe('')
    expect(held('Gateways')).toBe('')
  })

  it('keeps the placeholders when the overview fails', async () => {
    api.overview.mockRejectedValue(new Error('overview failed'))
    const w = await mountDashboard()
    expect(w.get('[role=alert]').text()).toBe('overview failed')
    expect(card(w, 'System').text()).toContain('12%')
    expect(card(w, 'Router').attributes('aria-busy')).toBe('true')
    // A failed read teaches nothing: the next visit still draws the first one.
    expect(localStorage.getItem(SHAPE_KEY)).toBeNull()
  })
})
