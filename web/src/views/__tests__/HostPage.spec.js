import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import HostPage from '@/views/system/HostPage.vue'

vi.mock('@/lib/api', () => ({
  api: { host: { status: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = { RefreshButton: true }

/** A Rocky router with everything in place. */
function report(over = {}) {
  return {
    root: true,
    manager: 'dnf',
    distro: 'Rocky Linux 10.2',
    kernel: '6.12.0-55.el10',
    units: [
      { name: 'ostiole.service', active: 'active', enabled: 'enabled' },
      { name: 'ostiole-firewall.service', active: 'active', enabled: 'enabled' },
      { name: 'systemd-networkd.service', active: 'inactive', enabled: 'disabled' },
    ],
    present: { nft: true, dnsmasq: true, unbound: true, miniupnpd: false, pppd: true, tc: true },
    bluetooth: 'blocked',
    network: {
      backend: 'networkd',
      networkd: 'inactive',
      owned: false,
      managers: ['NetworkManager'],
    },
    firewalled: true,
    ...over,
  }
}

async function page(over = {}) {
  api.host.status.mockResolvedValue(report(over))
  const wrapper = mount(HostPage, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('HostPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('says what this router is, unit by unit', async () => {
    const wrapper = await page()
    const text = wrapper.text()
    expect(text).toContain('Rocky Linux 10.2')
    expect(text).toContain('6.12.0-55.el10')
    expect(text).toContain('ostiole-firewall.service')
    expect(text).toContain('systemd-networkd.service')
    // The daemons the install script puts on, split into what is there
    // and what is not.
    expect(text).toContain('nft dnsmasq unbound pppd tc')
    expect(text).toContain('miniupnpd')
    expect(text).toContain('ostiole repair')
    expect(text).toContain('Bluetooth')
    expect(text).toContain('blocked')
  })

  it('says when Bluetooth is back, and what to run', async () => {
    const wrapper = await page({ bluetooth: 'loaded' })
    expect(wrapper.text()).toMatch(/Bluetooth\s+loaded\. Run ostiole repair as root\./)
  })

  it('names who owns the addresses', async () => {
    expect((await page()).text()).toContain('owned by NetworkManager')
    const owned = await page({
      network: {
        backend: 'networkd',
        networkd: 'active',
        owned: true,
        managers: ['NetworkManager'],
      },
    })
    expect(owned.text()).toContain('owned by Ostiole')
  })

  // A waiting handover is a console matter: the page says which command
  // settles it rather than offering a button that may drop the session.
  it('points a pending handover at the console', async () => {
    const wrapper = await page({
      network: { backend: 'networkd', networkd: 'active', owned: false, pending: true },
    })
    expect(wrapper.text()).toContain('ostiole takeover --network --confirm')
  })

  it('says when the daemon is not root', async () => {
    const wrapper = await page({ root: false })
    expect(wrapper.text()).toContain('not running as root')
  })
})
