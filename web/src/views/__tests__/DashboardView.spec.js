import { RouterLinkStub, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import DashboardView from '@/views/DashboardView.vue'

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

describe('DashboardView', () => {
  /** @type {(value: object) => void} */
  let answer

  beforeEach(() => {
    setActivePinia(createPinia())
    // The polls, and the frames a row leaving a transition group waits out.
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame'] })
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

  /**
   * The real transition groups, whose keying the default stub would hide.
   * The header reads the route, and there is none here.
   */
  async function mountDashboard() {
    const w = mount(DashboardView, {
      global: {
        stubs: { RouterLink: RouterLinkStub, PageHeader: true, 'transition-group': false },
      },
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

  it('swaps the placeholders for what the overview says', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const w = await mountDashboard()
    expect(card(w, 'Gateways')).toBeUndefined()
    answer({
      ...overview,
      gateways: [{ name: 'wan', interface: 'eth0', online: true, latencyMs: 1, lossPercent: 0 }],
    })
    await flushPromises()
    vi.advanceTimersToNextFrame()
    vi.advanceTimersToNextFrame()

    expect(warn).not.toHaveBeenCalled()
    expect(card(w, 'Gateways').text()).toContain('wan')
    expect(w.find('[data-reading]').exists()).toBe(false)
    for (const title of ['Interfaces', 'Busiest rules', 'Services', 'Router']) {
      expect(card(w, title).attributes('aria-busy')).toBeUndefined()
    }
    expect(card(w, 'Interfaces').text()).toContain('No interfaces.')
    expect(card(w, 'Busiest rules').text()).toContain('9 packets')
    expect(card(w, 'Router').text()).toContain('3 rules in 2 zones')
  })
})
