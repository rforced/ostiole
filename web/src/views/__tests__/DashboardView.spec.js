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

  it('shows nothing under the header until the overview is in', async () => {
    const w = await mountDashboard()
    // The usage has been read, but its card would be pushed down by the
    // warnings and interfaces the overview brings.
    expect(api.systemStats).toHaveBeenCalled()
    expect(w.findAll('section')).toHaveLength(0)
    expect(w.text()).toContain('Reading…')
  })

  it('lays every card out at once when the overview comes', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const w = await mountDashboard()
    answer({
      ...overview,
      gateways: [{ name: 'wan', interface: 'eth0', online: true, latencyMs: 1, lossPercent: 0 }],
    })
    await flushPromises()

    expect(warn).not.toHaveBeenCalled()
    expect(w.text()).not.toContain('Reading…')
    expect(w.find('[data-reading]').exists()).toBe(false)
    const titles = w.findAll('section h2').map((h) => h.text())
    expect(titles).toEqual([
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
    expect(card(w, 'System').text()).toContain('12%')
    expect(card(w, 'Gateways').text()).toContain('wan')
    expect(card(w, 'Interfaces').text()).toContain('No interfaces.')
    expect(card(w, 'Busiest rules').text()).toContain('9 packets')
    expect(card(w, 'Router').text()).toContain('3 rules in 2 zones')
  })

  it('shows what there is when the overview fails', async () => {
    api.overview.mockRejectedValue(new Error('overview failed'))
    const w = await mountDashboard()
    expect(w.get('[role=alert]').text()).toBe('overview failed')
    expect(card(w, 'System').text()).toContain('12%')
    expect(card(w, 'Router').attributes('aria-busy')).toBe('true')
  })
})
