import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import WireguardPage from '@/views/vpn/WireguardPage.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal()
  return { ...mod, api: { ...mod.api, wireguard: { status: vi.fn() } } }
})

// The page takes its tabs from the route; there is no router here.
vi.mock('@/lib/tabs', async () => {
  const { ref } = await import('vue')
  return { usePageTabs: () => ({ tabs: [], tab: ref('tunnels') }) }
})

const stubs = { PageHeader: true, ConfirmButton: true, PeerDialog: true, TunnelDialog: true }

const LAPTOP = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg='
const SITE = 'TrMvSoP4jYQlY6RIzBgbssQqY3vxI2Pi+y71lOWWXX0='
const PHONE = 'gN65BkIKy1eCE9pP1wdc8ROUtkHLF2PfAqYdyYBz6EA='

const tunnel = (name, peers) => ({
  name,
  zone: name,
  enabled: true,
  ipv4: { mode: 'static', address: '10.66.0.1/24' },
  ipv6: { mode: 'none' },
  wireguard: { publicKey: 'x', listenPort: 51820, peers },
})

function mountPage() {
  useConfigStore().replaceDraft({
    version: 11,
    zones: [{ name: 'wg0' }, { name: 'wg1' }],
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
      tunnel('wg1', []),
    ],
    rules: [],
  })
  return mount(WireguardPage, { global: { stubs } })
}

const row = (wrapper, name) => wrapper.findAll('tbody tr').find((tr) => tr.text().includes(name))

describe('WireguardPage status', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('shows each tunnel and peer as its device has them', async () => {
    let answer
    api.wireguard.status.mockReturnValue(new Promise((resolve) => (answer = resolve)))
    const wrapper = mountPage()
    await flushPromises()
    // Nothing is claimed before the first read.
    expect(row(wrapper, 'laptop').find('[data-label="Last handshake"]').text()).toBe('…')
    expect(wrapper.text()).not.toMatch(/\bup\b|\bdown\b/)

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
            lastHandshake: '2026-09-27T18:00:00Z',
            rxBytes: 1500,
            txBytes: 2000,
          },
          { name: 'site', publicKey: SITE, endpoint: '203.0.113.9:51820', rxBytes: 0, txBytes: 92 },
        ],
      },
      { name: 'wg1', up: false, peers: [] },
    ])
    await flushPromises()

    const titles = wrapper.findAll('.badge').map((b) => b.text())
    expect(titles).toContain('up')
    expect(titles).toContain('down')
    const laptop = row(wrapper, 'laptop')
    expect(laptop.find('[data-label="Last handshake"]').text()).toBe(
      new Date('2026-09-27T18:00:00Z').toLocaleString(),
    )
    expect(laptop.find('[data-label="Traffic"]').text()).toContain('↓ 1.5 kB')
    expect(laptop.find('[data-label="Traffic"]').text()).toContain('↑ 2.0 kB')
    expect(laptop.find('[data-label="Endpoint"]').text()).toBe('198.51.100.7:40000')
    // A peer dialled by name shows where it answered, and what it is set to.
    const site = row(wrapper, 'site')
    expect(site.find('[data-label="Last handshake"]').text()).toBe('never')
    expect(site.find('[data-label="Endpoint"]').text()).toContain('203.0.113.9:51820')
    expect(site.find('[data-label="Endpoint"]').text()).toContain('set to vpn.example.com:51820')
    // A disabled peer is not on the device.
    expect(row(wrapper, 'phone').find('[data-label="Traffic"]').text()).toBe('—')
  })

  it('says so when the tunnels cannot be read', async () => {
    api.wireguard.status.mockRejectedValue(new Error('operation not permitted'))
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('operation not permitted')
  })
})
