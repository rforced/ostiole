import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import SettingsTab from '@/views/vpn/tailscale/SettingsTab.vue'
import TailscaleStatus from '@/views/vpn/tailscale/TailscaleStatus.vue'

vi.mock('@/lib/api', () => ({
  api: { tailscale: { status: vi.fn(), login: vi.fn(), logout: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = { ConfirmButton: true, RouterLink: true }

function node(over = {}) {
  return {
    name: 'tailscale0',
    zone: 'tailnet',
    enabled: true,
    ipv4: { mode: 'none' },
    ipv6: { mode: 'none' },
    tailscale: { port: 41641, ...over },
  }
}

function config(over = {}) {
  return {
    version: 5,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }, { name: 'tailnet' }],
    interfaces: [
      {
        name: 'eth1',
        zone: 'lan',
        enabled: true,
        ipv4: { mode: 'static', address: '192.168.1.1/24' },
      },
    ],
    rules: [],
    ...over,
  }
}

function status(over = {}) {
  return { setUp: true, running: true, state: 'Running', ips: [], health: [], peers: [], ...over }
}

/** Mounts the strip with a saved and a draft configuration of our choosing. */
async function strip(st, { draft = config(), saved = config() } = {}) {
  api.tailscale.status.mockResolvedValue(st)
  const store = useConfigStore()
  store.draft = draft
  store.saved = saved
  store.loaded = true
  const wrapper = mount(TailscaleStatus, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('TailscaleStatus', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('names the command when the daemon is not on the router', async () => {
    const wrapper = await strip(status({ setUp: false, running: false, state: '' }))
    expect(wrapper.text()).toContain('ostiole repair --tailscale')
  })

  it('says the router has not joined when the draft has no interface', async () => {
    const wrapper = await strip(status({ running: false, state: '' }))
    expect(wrapper.text()).toContain('Not joined.')
  })

  it('asks for an apply while the interface is only in the draft', async () => {
    const wrapper = await strip(status({ running: false, state: '' }), {
      draft: config({ interfaces: [node()] }),
    })
    expect(wrapper.text()).toContain('Apply the draft to start it.')
  })

  it('offers both ways in when the node is not logged in', async () => {
    const both = config({ interfaces: [node()] })
    const wrapper = await strip(status({ state: 'NeedsLogin' }), { draft: both, saved: both })
    expect(wrapper.text()).toContain('Not logged in.')
    expect(wrapper.find('#ts-auth-key').exists()).toBe(true)

    api.tailscale.login.mockResolvedValue({ authUrl: 'https://login.tailscale.com/a/abc' })
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Log in')
      .trigger('click')
    await flushPromises()
    expect(api.tailscale.login).toHaveBeenCalledWith('')
    const link = wrapper.find('a')
    expect(link.attributes('href')).toBe('https://login.tailscale.com/a/abc')
    expect(wrapper.text()).toContain('Waiting for the login.')
  })

  it('shows the node and its tailnet once it is up', async () => {
    const both = config({ interfaces: [node()] })
    const wrapper = await strip(
      status({
        dnsName: 'fw.tail1.ts.net.',
        ips: ['100.101.102.103'],
        tailnet: 'example.com',
        keyExpiry: new Date(Date.now() + 3 * 86400000).toISOString(),
      }),
      { draft: both, saved: both },
    )
    expect(wrapper.text()).toContain('fw.tail1.ts.net.')
    expect(wrapper.text()).toContain('100.101.102.103')
    expect(wrapper.text()).toContain('tailnet example.com')
    expect(wrapper.text()).toContain('Key expires')
  })

  it('says nothing about a key that is nowhere near expiring', async () => {
    const both = config({ interfaces: [node()] })
    const wrapper = await strip(
      status({ keyExpiry: new Date(Date.now() + 180 * 86400000).toISOString() }),
      { draft: both, saved: both },
    )
    expect(wrapper.text()).not.toContain('Key expires')
  })
})

describe('Tailscale SettingsTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('joins a tailnet with the daemon-owned defaults', async () => {
    const store = useConfigStore()
    store.draft = config()
    store.loaded = true
    const wrapper = mount(SettingsTab, { global: { stubs } })

    await wrapper.find('button').trigger('click')
    expect(store.tailscale).toMatchObject({
      name: 'tailscale0',
      enabled: true,
      zone: 'lan',
      ipv4: { mode: 'none' },
      ipv6: { mode: 'none' },
      tailscale: { port: 41641 },
    })
  })

  it('offers a zone its networks as routes, masked', async () => {
    const store = useConfigStore()
    store.draft = config({ interfaces: [...config().interfaces, node()] })
    store.loaded = true
    const wrapper = mount(SettingsTab, { global: { stubs } })

    const button = wrapper.findAll('button').find((b) => b.text() === "Add lan's networks")
    await button.trigger('click')
    expect(store.tailscale.tailscale.advertiseRoutes).toEqual(['192.168.1.0/24'])
  })

  it('drops a preference from the draft when it is cleared', async () => {
    const store = useConfigStore()
    store.draft = config({ interfaces: [node({ hostname: 'gateway' })] })
    store.loaded = true
    const wrapper = mount(SettingsTab, { global: { stubs } })

    await wrapper.find('#ts-hostname').setValue('')
    expect('hostname' in store.tailscale.tailscale).toBe(false)
  })
})
