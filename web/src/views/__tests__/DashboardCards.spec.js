import { RouterLinkStub, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import GatewaysCard from '@/views/dashboard/GatewaysCard.vue'
import SystemLoadCard from '@/views/dashboard/SystemLoadCard.vue'

/** A small router with a quarter of its memory and a few connections in use. */
function reading(over = {}) {
  return {
    cpuPercent: 12.4,
    cores: 2,
    load1: 0.1,
    load5: 0.05,
    load15: 0.01,
    memTotal: 4_000_000_000,
    memAvailable: 3_000_000_000,
    swapTotal: 0,
    swapFree: 0,
    uptimeSeconds: 90_000,
    filesystems: [{ path: '/', total: 20_000_000_000, free: 15_000_000_000 }],
    conntrack: { count: 2113, max: 65536 },
    ...over,
  }
}

const meters = (w) => w.findAll('[role="meter"]').map((m) => m.attributes('aria-label'))

describe('SystemLoadCard', () => {
  it('keeps its shape before the first reading', () => {
    const w = mount(SystemLoadCard, { props: { stats: null } })
    expect(meters(w)).toEqual(['CPU usage', 'Memory usage', 'Disk usage'])
    expect(w.text()).toContain('no reading yet')
  })

  it('shows the connection table against its limit', () => {
    const w = mount(SystemLoadCard, { props: { stats: reading() } })
    expect(meters(w)).toContain('States usage')
    const states = w.find('[data-meter="States"]')
    expect(states.text()).toContain('3%')
    expect(states.text()).toContain('2,113 of 65,536')
    expect(states.find('[role="meter"]').attributes('aria-valuenow')).toBe('3')
  })

  it('leaves the connection table out when the kernel has none', () => {
    const w = mount(SystemLoadCard, { props: { stats: reading({ conntrack: undefined }) } })
    expect(meters(w)).toEqual(['CPU usage', 'Memory usage', 'Disk usage'])
    expect(w.text()).not.toContain('States')
  })

  it('warns when the connection table is nearly full', () => {
    const w = mount(SystemLoadCard, {
      props: { stats: reading({ conntrack: { count: 60_000, max: 65536 } }) },
    })
    const states = w.find('[data-meter="States"]')
    expect(states.text()).toContain('critical')
    expect(states.find('.meter-fill').classes()).toContain('bg-red-600')
  })
})

describe('GatewaysCard', () => {
  const stubs = { RouterLink: RouterLinkStub }
  const watched = {
    name: 'gw_wan',
    interface: 'wan0',
    address: '203.0.113.1',
    online: true,
    active: true,
    latencyMs: 12.34,
    lossPercent: 0,
  }
  const stray = {
    address: 'fe80::1',
    interface: 'wan1',
    family: 'IPv6',
    metric: 1024,
    protocol: 'ra',
  }

  it('lists a watched gateway with its health', () => {
    const w = mount(GatewaysCard, { props: { gateways: [watched] }, global: { stubs } })
    expect(w.text()).toContain('gw_wan')
    expect(w.text()).toContain('12.3 ms')
    expect(w.text()).toContain('active')
    expect(w.find('[data-unwatched]').exists()).toBe(false)
    expect(w.text()).toContain('Manage gateways')
  })

  it('lists an unwatched default route and says what that costs', () => {
    const w = mount(GatewaysCard, {
      props: { gateways: [watched], unwatched: [stray] },
      global: { stubs },
    })
    const row = w.find('[data-unwatched]')
    expect(row.text()).toContain('wan1')
    expect(row.text()).toContain('fe80::1 · ra')
    expect(row.text()).toContain('not watched')
    expect(w.text()).toContain('cannot fail over')
    expect(w.findComponent(RouterLinkStub).props('to')).toBe('/routing')
  })
})
