import { RouterLinkStub, flushPromises, mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import type {
  HealthResponse,
  Overview,
  Stats,
  TrafficLink,
  TrafficLinks,
  UpdateStatusReply,
} from '@/lib/types.gen'
import { fixture, must } from '@/test/fixtures'
import type { DeepPartial } from '@/test/fixtures'
import DashboardView from '@/views/DashboardView.vue'
import { DEFAULT_ORDER, LAYOUT_KEY } from '@/views/dashboard/layout'
import LayoutDialog from '@/views/dashboard/LayoutDialog.vue'
import { SHAPE_KEY } from '@/views/dashboard/shape'

vi.mock('@/lib/api', () => ({
  api: {
    health: vi.fn(),
    overview: vi.fn(),
    status: vi.fn(),
    systemStats: vi.fn(),
    traffic: { interfaces: vi.fn() },
    update: { status: vi.fn() },
  },
}))

class FakeSource {
  static CLOSED = 2
  close() {}
}

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
const card = (w: VueWrapper, title: string) =>
  must(w.findAll('section').find((s) => s.find('h2').exists() && s.find('h2').text() === title))
/** Every card's title, in page order. */
const titles = (w: VueWrapper) => w.findAll('section h2').map((h) => h.text())
/** A card's placeholder rows. */
const reading = (w: VueWrapper, title: string) => card(w, title).findAll('tr[data-reading]')

describe('DashboardView', () => {
  let answer: (value: DeepPartial<Overview>) => void

  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    // The polls.
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    vi.mocked(api.health).mockResolvedValue({ status: 'ok', version: 'v1.0.0' })
    vi.mocked(api.status).mockResolvedValue({
      configured: true,
      tableLoaded: true,
      network: 'networkd',
    })
    vi.mocked(api.systemStats).mockResolvedValue(fixture<Stats>(stats))
    vi.mocked(api.update.status).mockResolvedValue(fixture<UpdateStatusReply>({}))
    vi.mocked(api.overview).mockReturnValue(
      new Promise<Overview>((resolve) => (answer = (v) => resolve(fixture<Overview>(v)))),
    )
    vi.mocked(api.traffic.interfaces).mockResolvedValue({
      window: '5m',
      now: new Date().toISOString(),
      links: [fixture<TrafficLink>({ name: 'eth0', down: 8000, up: 4000, points: [] })],
    })
    vi.stubGlobal('EventSource', FakeSource)
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  /** The header reads the route, and there is none here. */
  async function mountDashboard() {
    const w = mount(DashboardView, {
      global: { stubs: { RouterLink: RouterLinkStub, PageHeader: true } },
    })
    await flushPromises()
    return w
  }

  // The cards fill in together. The usage meters filled in first, while
  // every other card was still grey.
  it('holds the usage meters until the overview is in', async () => {
    const w = await mountDashboard()
    for (const title of ['Interfaces', 'System', 'Busiest rules', 'Services', 'Router']) {
      expect(card(w, title).attributes('aria-busy')).toBe('true')
      expect(card(w, title).text()).toContain('Reading…')
    }
    expect(card(w, 'System').text()).not.toContain('12%')
    expect(card(w, 'System').text()).not.toContain('0.10')
    expect(card(w, 'Busiest rules').text()).not.toContain('0 packets')
    expect(card(w, 'Router').text()).not.toContain('0 rules')

    answer(overview)
    await flushPromises()
    expect(card(w, 'System').attributes('aria-busy')).toBeUndefined()
    expect(card(w, 'System').text()).toContain('12%')
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
      'Recent leases',
      'Router',
    ])
    expect(w.findAll('[data-reading] > .rounded-lg')).toHaveLength(2)
    expect(reading(w, 'Interfaces')).toHaveLength(4)
    expect(reading(w, 'Gateways')).toHaveLength(2)
    expect(reading(w, 'Busiest rules')).toHaveLength(1)
    // No leases last time: one line, as tall as the one that says so.
    expect(reading(w, 'Recent leases')).toHaveLength(1)
    expect(reading(w, 'Recent leases')[0].find('td').attributes('colspan')).toBe('3')
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
    expect(JSON.parse(localStorage.getItem(SHAPE_KEY) ?? 'null')).toEqual({
      warnings: 0,
      interfaces: 0,
      rules: 0,
      services: 0,
      cards: { gateways: 1 },
      // jsdom lays nothing out, so there is no height to keep.
      heights: {},
    })
  })

  // The Traffic card and the Router card's version come from reads of their
  // own. Whichever answers last, every card fills in with it.
  it('fills every card in at once, when the last read answers', async () => {
    localStorage.setItem(SHAPE_KEY, JSON.stringify({ cards: { traffic: 2 } }))
    let charts: (value: TrafficLinks) => void = () => undefined
    vi.mocked(api.traffic.interfaces).mockReturnValue(
      new Promise<TrafficLinks>((resolve) => (charts = resolve)),
    )
    let version: (value: HealthResponse) => void = () => undefined
    vi.mocked(api.health).mockReturnValue(
      new Promise<HealthResponse>((resolve) => (version = resolve)),
    )
    const w = await mountDashboard()
    expect(card(w, 'Traffic').attributes('aria-busy')).toBe('true')
    expect(card(w, 'Traffic').findAll('[data-reading]')).toHaveLength(2)

    const busy = () => w.findAll('section[aria-busy="true"]').map((s) => s.get('h2').text())
    const all = ['Interfaces', 'System', 'Traffic', 'Busiest rules', 'Services', 'Router']
    answer({
      ...overview,
      interfaces: [{ name: 'eth0', zone: 'wan', external: true, present: true, configured: true }],
    })
    await flushPromises()
    expect(busy()).toEqual(all)
    version({ status: 'ok', version: 'v1.0.0' })
    await flushPromises()
    expect(busy()).toEqual(all)
    charts({
      window: '5m',
      now: new Date().toISOString(),
      links: [fixture<TrafficLink>({ name: 'eth0', down: 8000, up: 4000, points: [] })],
    })
    await flushPromises()

    expect(busy()).toEqual([])
    expect(w.find('[data-reading]').exists()).toBe(false)
    expect(card(w, 'Traffic').get('svg[role="img"]').attributes('aria-label')).toBe(
      'eth0: down 8 kbit/s, up 4 kbit/s',
    )
    expect(card(w, 'System').text()).toContain('12%')
    expect(card(w, 'Router').text()).toContain('v1.0.0')
  })

  it('holds the room each card took last time, until it is read', async () => {
    localStorage.setItem(
      SHAPE_KEY,
      JSON.stringify({ cards: { gateways: 1 }, heights: { interfaces: 290, gateways: 334 } }),
    )
    const w = await mountDashboard()
    const held = (title: string) => card(w, title).element.style.minHeight
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
    vi.mocked(api.overview).mockRejectedValue(new Error('overview failed'))
    const w = await mountDashboard()
    expect(w.get('[role=alert]').text()).toBe('overview failed')
    // The usage was read, and waits with the rest for the next poll.
    expect(card(w, 'System').attributes('aria-busy')).toBe('true')
    expect(card(w, 'System').text()).not.toContain('12%')
    expect(card(w, 'Router').attributes('aria-busy')).toBe('true')
    // A failed read teaches nothing: the next visit still draws the first one.
    expect(localStorage.getItem(SHAPE_KEY)).toBeNull()
  })

  describe('with a layout of its own', () => {
    const keep = (layout: object) => localStorage.setItem(LAYOUT_KEY, JSON.stringify(layout))

    it('draws the cards in its order, without the hidden ones', async () => {
      localStorage.setItem(SHAPE_KEY, JSON.stringify({ cards: { gateways: 1, leases: 0 } }))
      keep({
        order: ['router', 'leases', 'services', 'gateways', 'rules', 'system'],
        hidden: ['gateways', 'rules'],
      })
      const w = await mountDashboard()
      expect(titles(w)).toEqual(['Interfaces', 'Router', 'Recent leases', 'Services', 'System'])
      answer({ ...overview, dhcp: { enabled: true } })
      await flushPromises()
      expect(titles(w)).toEqual(['Interfaces', 'Router', 'Recent leases', 'Services', 'System'])
    })

    it('reads no usage while the System card is hidden, and reads it once shown', async () => {
      keep({ order: DEFAULT_ORDER, hidden: ['system'] })
      const w = await mountDashboard()
      answer(overview)
      await flushPromises()
      expect(titles(w)).not.toContain('System')
      expect(api.systemStats).not.toHaveBeenCalled()

      w.findComponent(LayoutDialog).vm.$emit('change', { order: [...DEFAULT_ORDER], hidden: [] })
      await flushPromises()
      expect(api.systemStats).toHaveBeenCalledTimes(1)
      expect(card(w, 'System').text()).toContain('12%')
      expect(JSON.parse(localStorage.getItem(LAYOUT_KEY) ?? 'null')).toEqual({
        order: [...DEFAULT_ORDER],
        hidden: [],
      })
    })

    it('fills the cards in without waiting on a hidden Traffic card', async () => {
      localStorage.setItem(SHAPE_KEY, JSON.stringify({ cards: { traffic: 2 } }))
      keep({ order: DEFAULT_ORDER, hidden: ['traffic'] })
      vi.mocked(api.traffic.interfaces).mockReturnValue(new Promise(() => {}))
      const w = await mountDashboard()
      answer({
        ...overview,
        interfaces: [
          { name: 'eth0', zone: 'wan', external: true, present: true, configured: true },
        ],
      })
      await flushPromises()
      expect(titles(w)).not.toContain('Traffic')
      expect(w.findAll('section[aria-busy="true"]')).toHaveLength(0)
      expect(api.traffic.interfaces).not.toHaveBeenCalled()
    })

    it('moves a card when the dialog says so', async () => {
      const w = await mountDashboard()
      answer(overview)
      await flushPromises()
      w.findComponent(LayoutDialog).vm.$emit('change', {
        order: ['router', ...DEFAULT_ORDER.filter((n) => n !== 'router')],
        hidden: [],
      })
      await flushPromises()
      expect(titles(w)).toEqual(['Interfaces', 'Router', 'System', 'Busiest rules', 'Services'])
    })
  })
})
