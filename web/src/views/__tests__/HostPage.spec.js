import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import HostPage from '@/views/system/HostPage.vue'

vi.mock('@/lib/api', () => ({
  api: { host: { status: vi.fn(), flushLegacy: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = { ConfirmButton: true, RefreshButton: true }

/** A Rocky router with everything in place and one leftover table. */
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
    legacy: {
      tables: [
        { backend: 'nft', family: 'ip', name: 'filter', rules: 0, chains: ['INPUT'] },
        {
          backend: 'nft',
          family: 'ip',
          name: 'nat',
          rules: 0,
          chains: ['DOCKER'],
          owner: 'Docker',
        },
      ],
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

  it('clears the leftovers nothing else owns, and offers the owned one separately', async () => {
    const wrapper = await page()
    api.host.flushLegacy.mockResolvedValue({ output: 'cleared ip filter', status: report() })
    const buttons = wrapper.findAllComponents({ name: 'ConfirmButton' })
    // One sweep for the unowned table, one "clear anyway" for Docker's.
    expect(buttons).toHaveLength(2)
    expect(buttons[0].props('label')).toBe('Clear leftovers')
    expect(buttons[1].props('typed')).toBe('nat')
    buttons[0].vm.$emit('confirm')
    await flushPromises()
    expect(api.host.flushLegacy).toHaveBeenCalledWith([])
    expect(wrapper.text()).toContain('cleared ip filter')
  })

  it('offers nothing to press on a daemon that is not root', async () => {
    const wrapper = await page({ root: false })
    expect(wrapper.text()).toContain('not running as root')
    expect(wrapper.findAllComponents({ name: 'ConfirmButton' })).toHaveLength(0)
  })

  it('shows why a flush failed', async () => {
    const wrapper = await page()
    api.host.flushLegacy.mockRejectedValue(new Error('not permitted'))
    wrapper.findAllComponents({ name: 'ConfirmButton' })[0].vm.$emit('confirm')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('not permitted')
  })
})
