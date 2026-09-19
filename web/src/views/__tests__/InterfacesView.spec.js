import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import InterfacesView from '@/views/InterfacesView.vue'

// The page only needs its tabs to exist; the router is not what is under
// test here.
vi.mock('@/lib/tabs', () => ({
  usePageTabs: () => ({ tabs: [], tab: ref('interfaces') }),
}))

vi.mock('@/lib/api', () => ({
  api: {
    interfaces: { live: vi.fn() },
    services: { status: vi.fn() },
    config: { get: vi.fn() },
  },
  ApiError: class ApiError extends Error {},
}))

// The tab wrappers have to render their slot or the tables are not there.
const slotted = { template: '<div><slot /></div>' }

const stubs = {
  AppTabs: slotted,
  TabsContent: slotted,
  ConfirmButton: true,
  InterfaceDialog: true,
  VlanDialog: true,
  PppoeDialog: true,
  AggregateDialog: true,
  ZoneDialog: true,
  RouterLink: { props: ['to'], template: '<a :to="to"><slot /></a>' },
}

describe('InterfacesView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.interfaces.live.mockResolvedValue([])
    api.services.status.mockResolvedValue({ pppoeSetUp: true })
    api.config.get.mockResolvedValue({ version: 3, interfaces: [], zones: [] })
  })

  // An apply creates and destroys real devices. Without this the table
  // keeps showing a VLAN that the apply just removed from the kernel.
  it('re-reads the live links after an apply', async () => {
    mount(InterfacesView, { global: { stubs } })
    await nextTick()
    expect(api.interfaces.live).toHaveBeenCalledTimes(1)

    useConfigStore().markApplied()
    // The re-read queues behind the first read if that is still in flight.
    await vi.waitFor(() => expect(api.interfaces.live).toHaveBeenCalledTimes(2))
  })

  /** A router with one of everything that has a page of its own. */
  function draft() {
    return {
      version: 5,
      zones: [{ name: 'lan' }, { name: 'tailnet' }],
      interfaces: [
        {
          name: 'eth1',
          zone: 'lan',
          enabled: true,
          ipv4: { mode: 'static', address: '10.0.0.1/24' },
        },
        {
          name: 'wg0',
          zone: 'lan',
          enabled: true,
          ipv4: { mode: 'static', address: '10.66.0.1/24' },
          wireguard: { privateKey: 'k', listenPort: 51820, peers: [{ name: 'laptop' }] },
        },
        {
          name: 'tailscale0',
          zone: 'tailnet',
          enabled: true,
          ipv4: { mode: 'none' },
          tailscale: { port: 41641 },
        },
        {
          name: 'ap0',
          zone: 'lan',
          enabled: true,
          ipv4: { mode: 'none' },
          wireless: { radio: 'wlp3s0', ssid: 'ostiole-lan', security: 'wpa2-wpa3' },
        },
      ],
      rules: [],
    }
  }

  function rowFor(wrapper, name) {
    return wrapper.findAll('tr').find((r) => r.find('td')?.exists() && r.text().includes(name))
  }

  // A tunnel and a tailnet node are made on their own page and deleted
  // there. Deleting from here offered a dialog that was wrong twice over:
  // it promised the device would stay, and it never listed the peers.
  it('sends a VPN interface to its own page instead of deleting it here', async () => {
    const config = useConfigStore()
    config.draft = draft()
    config.loaded = true
    const wrapper = mount(InterfacesView, { global: { stubs } })
    await flushPromises()

    for (const [name, label, to] of [
      ['wg0', 'WireGuard', '/vpn/wireguard'],
      ['tailscale0', 'Tailscale', '/vpn/tailscale'],
      ['ap0', 'Wireless', '/wireless'],
    ]) {
      const row = rowFor(wrapper, name)
      expect(row.findComponent({ name: 'ConfirmButton' }).exists()).toBe(false)
      const link = row.find('a')
      expect(link.attributes('to')).toBe(to)
      expect(link.text()).toBe(label)
    }

    // An ordinary interface is still removed from here.
    const plain = rowFor(wrapper, 'eth1')
    expect(plain.findComponent({ name: 'ConfirmButton' }).exists()).toBe(true)
    expect(rowFor(wrapper, 'ap0').text()).toContain('network "ostiole-lan" on wlp3s0')
  })
})
