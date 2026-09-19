import { RouterLinkStub, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import GatewaysCard from '@/views/dashboard/GatewaysCard.vue'
import RecentBlocksCard from '@/views/dashboard/RecentBlocksCard.vue'
import RecentLeasesCard from '@/views/dashboard/RecentLeasesCard.vue'
import RouterCard from '@/views/dashboard/RouterCard.vue'
import SystemLoadCard from '@/views/dashboard/SystemLoadCard.vue'
import WirelessCard from '@/views/dashboard/WirelessCard.vue'

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
/** Body rows; the test renderer stubs the transition group that would wrap them. */
const rows = (w) => w.findAll('tr').filter((r) => r.find('td').exists())

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

describe('RouterCard', () => {
  const stubs = { RouterLink: RouterLinkStub }

  it('folds the firewall and the daemon into one card', () => {
    const w = mount(RouterCard, {
      props: {
        status: { configured: true, tableLoaded: true, network: 'networkd' },
        summary: { hostname: 'fw', rules: 3, zones: 2, revisions: 4 },
        health: { status: 'ok', version: 'v0.9.0', commit: 'abc1234' },
        update: { latest: 'v0.9.1' },
      },
      global: { stubs },
    })
    const text = w.text()
    expect(text).toContain('fw')
    expect(text).toContain('v0.9.0')
    expect(text).toContain('abc1234')
    expect(text).toContain('loaded')
    expect(text).toContain('3 rules in 2 zones')
    expect(text).toContain('networkd')
    expect(text).toContain('v0.9.1')
    expect(text).toContain('4 ·')
  })

  it('says when the ruleset is not in the kernel', () => {
    const w = mount(RouterCard, {
      props: { status: { configured: true, tableLoaded: false, network: 'none' }, health: {} },
      global: { stubs },
    })
    expect(w.text()).toContain('not loaded')
    expect(w.text()).not.toContain('Update')
  })
})

describe('RecentBlocksCard', () => {
  const stubs = { RouterLink: RouterLinkStub }

  it('lists refusals with who refused them', () => {
    const w = mount(RecentBlocksCard, {
      props: {
        blocks: [
          {
            time: '2026-09-19T12:00:00Z',
            kind: 'default-drop',
            proto: 'tcp',
            in: 'wan0',
            src: '203.0.113.9',
            srcPort: 51000,
            dst: '198.51.100.1',
            dstPort: 22,
          },
          {
            time: '2026-09-19T11:59:00Z',
            kind: 'rule',
            ruleId: 'block-iot',
            proto: 'udp',
            src: '10.0.0.5',
            dst: '8.8.8.8',
            dstPort: 53,
          },
        ],
      },
      global: { stubs },
    })
    const body = rows(w)
    expect(body).toHaveLength(2)
    expect(body[0].text()).toContain('203.0.113.9:51000')
    expect(body[0].text()).toContain('default drop')
    expect(body[0].text()).toContain('tcp · wan0')
    expect(body[1].text()).toContain('block-iot')
    expect(w.findComponent(RouterLinkStub).props('to')).toBe('/firewall/log')
  })

  it('says so when nothing was refused', () => {
    const w = mount(RecentBlocksCard, { props: { blocks: [] }, global: { stubs } })
    expect(w.text()).toContain('Nothing refused lately.')
  })
})

describe('RecentLeasesCard', () => {
  const stubs = { RouterLink: RouterLinkStub }

  it('names clients and says how long each lease has left', () => {
    const soon = new Date(Date.now() + 2 * 3600 * 1000 + 90_000).toISOString()
    const w = mount(RecentLeasesCard, {
      props: {
        leases: [
          { ip: '10.0.0.7', mac: 'aa:bb:cc:dd:ee:ff', hostname: 'laptop', expires: soon },
          { ip: '10.0.0.8', mac: '11:22:33:44:55:66', expires: '2020-01-01T00:00:00Z' },
          {
            ip: '10.0.0.9',
            mac: '99:88:77:66:55:44',
            static: true,
            expires: '1970-01-01T00:00:00Z',
          },
        ],
        total: 12,
      },
      global: { stubs },
    })
    const body = rows(w)
    expect(body[0].text()).toContain('laptop')
    expect(body[0].text()).toContain('2h 1m left')
    expect(body[1].text()).toContain('11:22:33:44:55:66')
    expect(body[1].text()).toContain('expired')
    expect(body[2].text()).toContain('static')
    expect(w.text()).toContain('All 12 leases')
    expect(w.findComponent(RouterLinkStub).props('to')).toBe('/services/dhcp#leases')
  })
})

describe('WirelessCard', () => {
  const stubs = { RouterLink: RouterLinkStub }
  const client = (i) => ({
    mac: `00:00:00:00:00:${String(i).padStart(2, '0')}`,
    interface: 'wlan0',
    ssid: 'Home',
    hostname: i === 1 ? 'phone' : '',
    address: i === 1 ? '10.0.0.20' : '',
    signalDbm: -50 - i,
    connectedSeconds: 3700,
  })

  it('counts clients per network and shows the first few', () => {
    const w = mount(WirelessCard, {
      props: {
        wireless: {
          networks: [
            { interface: 'wlan0', ssid: 'Home', clients: 10 },
            { interface: 'wlan1', ssid: '', clients: 0 },
          ],
          clients: Array.from({ length: 10 }, (_, i) => client(i + 1)),
        },
      },
      global: { stubs },
    })
    expect(w.text()).toContain('Home')
    expect(w.text()).toContain('10 clients')
    expect(w.text()).toContain('wlan1')
    expect(w.text()).toContain('0 clients')
    const body = rows(w)
    expect(body).toHaveLength(8)
    expect(body[0].text()).toContain('phone')
    expect(body[0].text()).toContain('10.0.0.20')
    expect(body[0].text()).toContain('-51 dBm')
    expect(body[0].text()).toContain('1h 1m')
    expect(w.text()).toContain('And 2 more')
    expect(w.findComponent(RouterLinkStub).props('to')).toBe('/wireless#clients')
  })

  it('shows a network with nobody on it', () => {
    const w = mount(WirelessCard, {
      props: {
        wireless: { networks: [{ interface: 'wlan0', ssid: 'Home', clients: 0 }], clients: [] },
      },
      global: { stubs },
    })
    expect(w.text()).toContain('No clients.')
    expect(w.text()).toContain('All clients')
  })
})
