import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import WireguardPage from '@/views/vpn/WireguardPage.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal()
  return { ...mod, api: { ...mod.api, wireguard: { status: vi.fn() }, gateways: vi.fn() } }
})

// The page takes its tabs from the route; there is no router here.
const tab = ref('tunnels')
vi.mock('@/lib/tabs', () => ({ usePageTabs: () => ({ tabs: [], tab }) }))

const stubs = {
  PageHeader: true,
  ConfirmButton: true,
  PeerDialog: true,
  TunnelDialog: true,
  RouterLink: { template: '<a><slot /></a>' },
}

const LAPTOP = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg='
const SITE = 'TrMvSoP4jYQlY6RIzBgbssQqY3vxI2Pi+y71lOWWXX0='
const PHONE = 'gN65BkIKy1eCE9pP1wdc8ROUtkHLF2PfAqYdyYBz6EA='
const PROVIDER = 'nKB29JG3cjuUIEOQMvYSmFDrx2lUT0LTwWyFmvu0jG8='

const tunnel = (name, peers, extra = {}) => ({
  name,
  zone: name,
  enabled: true,
  ipv4: { mode: 'static', address: '10.66.0.1/24' },
  ipv6: { mode: 'none' },
  wireguard: { publicKey: 'x', listenPort: 51820, peers },
  ...extra,
})

function mountPage() {
  useConfigStore().replaceDraft({
    version: 12,
    zones: [{ name: 'wg0' }, { name: 'wg1', external: true }],
    interfaces: [
      tunnel('wg0', [
        { name: 'laptop', enabled: true, publicKey: LAPTOP, allowedIps: ['10.66.0.2/32'] },
        {
          name: 'site',
          enabled: true,
          publicKey: SITE,
          allowedIps: ['10.66.0.3/32'],
          endpoint: 'vpn.example.com:51820',
        },
        { name: 'phone', enabled: false, publicKey: PHONE, allowedIps: ['10.66.0.4/32'] },
      ]),
      tunnel(
        'wg1',
        [{ name: 'provider', enabled: true, publicKey: PROVIDER, allowedIps: ['0.0.0.0/0'] }],
        { ipv4: { mode: 'static', address: '10.66.1.2/32' } },
      ),
    ],
    gateways: [{ name: 'vpn', enabled: true, interface: 'wg1', monitor: '10.64.0.1' }],
    rules: [],
  })
  return mount(WireguardPage, { global: { stubs } })
}

const row = (wrapper, name) => wrapper.findAll('tbody tr').find((tr) => tr.text().includes(name))

/** A handshake this long ago, as the device reports it. */
const ago = (seconds) => new Date(Date.now() - seconds * 1000).toISOString()

describe('WireguardPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.gateways.mockResolvedValue([])
  })

  it('shows each peer as its device has it', async () => {
    tab.value = 'peers'
    let answer
    api.wireguard.status.mockReturnValue(new Promise((resolve) => (answer = resolve)))
    const wrapper = mountPage()
    await flushPromises()
    // Nothing is claimed before the first read.
    expect(row(wrapper, 'laptop').find('[data-label="Last handshake"]').text()).toBe('…')
    expect(wrapper.text()).not.toMatch(/connected|quiet/)

    answer([
      {
        name: 'wg0',
        up: true,
        listenPort: 51820,
        peers: [
          {
            name: 'laptop',
            publicKey: LAPTOP,
            endpoint: '198.51.100.7:40000',
            lastHandshake: ago(30),
            rxBytes: 1500,
            txBytes: 2000,
          },
          { name: 'site', publicKey: SITE, endpoint: '203.0.113.9:51820', rxBytes: 0, txBytes: 92 },
        ],
      },
      { name: 'wg1', up: false, peers: [] },
    ])
    await flushPromises()

    const laptop = row(wrapper, 'laptop')
    expect(laptop.text()).toContain('connected')
    expect(laptop.find('[data-label="Tunnel"]').text()).toBe('wg0')
    expect(laptop.find('[data-label="Traffic"]').text()).toContain('↓ 1.5 kB')
    expect(laptop.find('[data-label="Traffic"]').text()).toContain('↑ 2.0 kB')
    expect(laptop.find('[data-label="Endpoint"]').text()).toBe('198.51.100.7:40000')
    // A peer dialled by name shows where it answered, and what it is set to.
    const site = row(wrapper, 'site')
    expect(site.text()).toContain('quiet')
    expect(site.find('[data-label="Last handshake"]').text()).toBe('never')
    expect(site.find('[data-label="Endpoint"]').text()).toContain('203.0.113.9:51820')
    expect(site.find('[data-label="Endpoint"]').text()).toContain('set to vpn.example.com:51820')
    // A disabled peer is not on the device.
    const phone = row(wrapper, 'phone')
    expect(phone.text()).toContain('disabled')
    expect(phone.find('[data-label="Traffic"]').text()).toBe('—')
  })

  it('counts the peers each tunnel has sending, and names its gateway', async () => {
    tab.value = 'tunnels'
    api.wireguard.status.mockResolvedValue([
      {
        name: 'wg0',
        up: true,
        peers: [
          { name: 'laptop', publicKey: LAPTOP, lastHandshake: ago(30), rxBytes: 1, txBytes: 1 },
          { name: 'site', publicKey: SITE, lastHandshake: ago(600), rxBytes: 1, txBytes: 1 },
        ],
      },
      { name: 'wg1', up: false, peers: [] },
    ])
    api.gateways.mockResolvedValue([{ name: 'vpn', tunnel: true, online: false, unknown: false }])
    const wrapper = mountPage()
    await flushPromises()

    const wg0 = row(wrapper, 'wg0')
    expect(wg0.text()).toContain('up')
    // Ten minutes since its handshake: quiet. The disabled phone is not counted.
    expect(wg0.find('[data-label="Peers"]').text()).toBe('1 of 2 connected')
    expect(wg0.find('[data-label="Gateway"]').text()).toBe('—')
    const wg1 = row(wrapper, 'wg1')
    expect(wg1.text()).toContain('down')
    expect(wg1.find('[data-label="Listening on"]').text()).toBe('udp/51820')
    expect(wg1.find('[data-label="Gateway"]').text()).toMatch(/vpn\s*down/)
  })

  it('says so when the tunnels cannot be read', async () => {
    tab.value = 'tunnels'
    api.wireguard.status.mockRejectedValue(new Error('operation not permitted'))
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('operation not permitted')
  })
})
