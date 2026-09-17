import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfirmStore } from '@/stores/confirm'
import HostPage from '@/views/system/HostPage.vue'

vi.mock('@/lib/api', () => ({
  api: {
    host: {
      status: vi.fn(),
      setup: vi.fn(),
      takeover: vi.fn(),
      removePackages: vi.fn(),
      flushLegacy: vi.fn(),
      network: vi.fn(),
      skipStep: vi.fn(),
    },
  },
  ApiError: class ApiError extends Error {},
}))

vi.mock('@/router', () => ({ SKIP_HOST_KEY: 'ostiole.skipHost' }))

const push = vi.fn()
vi.mock('vue-router', () => ({ useRouter: () => ({ push }) }))

const stubs = { ConfirmButton: true, RefreshButton: true }

/** A Rocky router with firewalld running, no dnsmasq, and a leftover table. */
function report(over = {}) {
  return {
    root: true,
    manager: 'dnf',
    distro: 'Rocky Linux 10.2',
    prepared: false,
    components: [
      {
        key: 'nft',
        label: 'nftables',
        needs: 'the firewall itself',
        required: true,
        why: 'every ruleset',
        present: true,
        ready: true,
        availability: 'installable',
        packages: ['nftables'],
      },
      {
        key: 'dnsmasq',
        label: 'DHCP and DNS',
        needs: 'handing out addresses',
        required: true,
        why: 'the DHCP service is turned on',
        present: false,
        ready: false,
        unit: 'ostiole-dnsmasq.service',
        availability: 'installable',
        packages: ['dnsmasq'],
      },
      {
        key: 'miniupnpd',
        label: 'Port mapping',
        needs: 'letting clients ask',
        required: false,
        present: false,
        ready: false,
        availability: 'unpackaged',
        note: 'The Red Hat family has no EPEL build.',
      },
    ],
    competitors: [
      {
        name: 'firewalld',
        kind: 'firewall',
        active: 'active',
        enabled: 'enabled',
        conflicts: true,
        packages: ['firewalld'],
        installed: true,
      },
      {
        name: 'NetworkManager',
        kind: 'network',
        active: 'inactive',
        enabled: 'disabled',
        conflicts: false,
        packages: ['NetworkManager'],
        installed: true,
      },
    ],
    legacy: {
      version: 'iptables v1.8.10 (nf_tables)',
      tables: [
        { family: 'ip', name: 'nat', backend: 'nft', chains: ['DOCKER'], owner: 'Docker' },
        { family: 'ip6', name: 'filter', backend: 'nft', chains: ['INPUT'] },
      ],
    },
    network: {
      backend: 'networkd',
      networkd: 'inactive',
      owned: false,
      managers: ['NetworkManager'],
    },
    steps: [
      { step: 'packages', state: 'outstanding', detail: 'DHCP and DNS is not set up' },
      { step: 'firewall', state: 'outstanding', detail: 'firewalld still filters traffic' },
      { step: 'legacy', state: 'outstanding', detail: 'ip6 filter was left behind' },
      { step: 'network', state: 'outstanding', detail: 'NetworkManager still owns the addresses' },
    ],
    ...over,
  }
}

async function page(over) {
  api.host.status.mockResolvedValue(report(over))
  const wrapper = mount(HostPage, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('HostPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    sessionStorage.clear()
  })

  it('names what each component is needed for and what state it is in', async () => {
    const wrapper = await page()
    const text = wrapper.text()
    expect(text).toContain('Rocky Linux 10.2')
    expect(text).toContain('the DHCP service is turned on')
    expect(text).toContain('ready')
    expect(text).toContain('not installed')
    // A component nothing packages here says so instead of offering a
    // button that cannot work.
    expect(text).toContain('not packaged here')
    expect(text).toContain('no EPEL build')
  })

  it('sets up only the components this router is missing', async () => {
    const wrapper = await page()
    api.host.setup.mockResolvedValue({ output: 'installed dnsmasq', status: report() })
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(api.host.setup).toHaveBeenCalledWith(['dnsmasq'])
    expect(wrapper.text()).toContain('installed dnsmasq')
  })

  it('offers to remove packages only for a competitor that has been retired', async () => {
    const wrapper = await page()
    const buttons = wrapper.findAllComponents({ name: 'ConfirmButton' })
    const labels = buttons.map((b) => b.props('label'))
    // firewalld is still running, so its packages are not on offer;
    // NetworkManager has been masked, so they are.
    expect(labels.filter((l) => l === 'Remove packages')).toHaveLength(1)
  })

  it('previews a removal, and removes nothing if the answer is no', async () => {
    const wrapper = await page()
    api.host.removePackages.mockResolvedValue({
      output: 'Removing: NetworkManager',
      status: report(),
    })
    const confirm = useConfirmStore()
    const removing = wrapper.vm.removePackages('NetworkManager', ['NetworkManager'])
    await flushPromises()
    // The package manager's own account of what would go is on the page
    // before the question is asked.
    expect(api.host.removePackages).toHaveBeenCalledWith(['NetworkManager'], true)
    expect(wrapper.text()).toContain('Removing: NetworkManager')
    expect(confirm.request.question).toBe('Remove NetworkManager?')
    // And the name has to be typed back, because this one cannot be undone.
    expect(confirm.request.typed).toBe('NetworkManager')
    confirm.settle(false)
    await removing
    expect(api.host.removePackages).toHaveBeenCalledTimes(1)
  })

  it('removes the packages once the answer is yes', async () => {
    const wrapper = await page()
    api.host.removePackages.mockResolvedValue({ output: 'Removed', status: report() })
    const confirm = useConfirmStore()
    const removing = wrapper.vm.removePackages('NetworkManager', ['NetworkManager'])
    await flushPromises()
    confirm.settle(true)
    await removing
    expect(api.host.removePackages).toHaveBeenLastCalledWith(['NetworkManager'], false)
  })

  it('leaves a leftover ruleset that belongs to something alone', async () => {
    const wrapper = await page()
    expect(wrapper.text()).toContain('Belongs to Docker, so it is left alone.')
    api.host.flushLegacy.mockResolvedValue({ output: 'deleted', status: report() })
    // The sweep asks for nothing in particular, which is what leaves the
    // owned table where it is.
    await wrapper.vm.flush([])
    expect(api.host.flushLegacy).toHaveBeenCalledWith([])
  })

  it('counts down a handover it started and can confirm it', async () => {
    vi.useFakeTimers()
    const wrapper = await page()
    api.host.network.mockResolvedValue({ output: 'switching', status: report() })
    await wrapper.vm.takeNetwork()
    await flushPromises()
    expect(api.host.network).toHaveBeenCalledWith('take', '180s')
    vi.advanceTimersByTime(2000)
    expect(wrapper.vm.countdown).toBe(178)
    await wrapper.vm.confirmNetwork()
    expect(api.host.network).toHaveBeenLastCalledWith('confirm')
    expect(wrapper.vm.countdown).toBe(0)
    vi.useRealTimers()
  })

  it('lets somebody past the gate for this session without changing the router', async () => {
    const wrapper = await page()
    await wrapper.find('button.link').trigger('click')
    expect(sessionStorage.getItem('ostiole.skipHost')).toBe('1')
    expect(push).toHaveBeenCalledWith('/')
    expect(api.host.skipStep).not.toHaveBeenCalled()
  })

  it('offers no buttons to a daemon that is not root', async () => {
    const wrapper = await page({ root: false })
    expect(wrapper.text()).toContain('not running as root')
    expect(wrapper.findAll('button.btn-primary')).toHaveLength(0)
  })
})
