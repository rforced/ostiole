import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import PeersTab from '@/views/vpn/tailscale/PeersTab.vue'
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

  it('says stopped when the daemon is not answering', async () => {
    const both = config({ interfaces: [node()] })
    const wrapper = await strip(status({ running: false, state: '' }), { draft: both, saved: both })
    expect(wrapper.text()).toContain('Stopped.')
  })

  it('waits for approval instead of offering a login', async () => {
    const both = config({ interfaces: [node()] })
    const wrapper = await strip(status({ state: 'NeedsMachineAuth' }), {
      draft: both,
      saved: both,
    })
    expect(wrapper.text()).toContain('Waiting for approval')
    expect(wrapper.find('#ts-auth-key').exists()).toBe(false)
  })

  it('logs in with a key and forgets it', async () => {
    const both = config({ interfaces: [node()] })
    const wrapper = await strip(status({ state: 'NeedsLogin' }), { draft: both, saved: both })
    api.tailscale.login.mockResolvedValue({ state: 'Running' })
    await wrapper.find('#ts-auth-key').setValue('tskey-auth-abc')
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Log in with key')
      .trigger('click')
    await flushPromises()
    expect(api.tailscale.login).toHaveBeenCalledWith('tskey-auth-abc')
    expect(wrapper.find('#ts-auth-key').element.value).toBe('')
  })

  it('logs out through the confirmation', async () => {
    const both = config({ interfaces: [node()] })
    const wrapper = await strip(status({ dnsName: 'fw.tail1.ts.net.' }), {
      draft: both,
      saved: both,
    })
    api.tailscale.logout.mockResolvedValue(status({ state: 'NeedsLogin' }))
    wrapper.findComponent(ConfirmButton).vm.$emit('confirm')
    await flushPromises()
    expect(api.tailscale.logout).toHaveBeenCalled()
    expect(wrapper.text()).toContain('Not logged in.')
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

/** TransitionGroup stubs out, so the tbody it renders as is not there. */
function bodyRows(wrapper) {
  return wrapper.findAll('tr').filter((r) => r.findAll('td').length > 0)
}

describe('Tailscale PeersTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // A peer's DERP home region is set whether or not anything is relayed
  // through it, and the direct endpoint is cleared when the connection
  // goes idle. Reading either on its own calls an idle direct peer
  // relayed, which is what `tailscale status` on that peer contradicts.
  it('names the path only while traffic is flowing', async () => {
    api.tailscale.status.mockResolvedValue(
      status({
        peers: [
          {
            hostName: 'direct',
            ips: [],
            routes: [],
            online: true,
            active: true,
            relay: 'ord',
            directAddr: '203.0.113.9:41641',
          },
          { hostName: 'relayed', ips: [], routes: [], online: true, active: true, relay: 'ord' },
          { hostName: 'dozing', ips: [], routes: [], online: true, active: false, relay: 'ord' },
        ],
      }),
    )
    const wrapper = mount(PeersTab, { global: { stubs } })
    await flushPromises()

    const rows = bodyRows(wrapper)
    expect(rows[0].text()).toContain('direct 203.0.113.9:41641')
    expect(rows[1].text()).toContain('relay ord')
    expect(rows[2].text()).toContain('idle')
    expect(rows[2].text()).not.toContain('relay')
  })

  // "online" and "last seen 20 minutes ago" cannot both be the answer.
  it('keeps a last seen time for the peers that are not there', async () => {
    const at = new Date(Date.now() - 3600000).toISOString()
    api.tailscale.status.mockResolvedValue(
      status({
        peers: [
          { hostName: 'here', ips: [], routes: [], online: true, active: false, lastSeen: at },
          { hostName: 'gone', ips: [], routes: [], online: false, active: false, lastSeen: at },
        ],
      }),
    )
    const wrapper = mount(PeersTab, { global: { stubs } })
    await flushPromises()

    const rows = bodyRows(wrapper)
    expect(rows[0].findAll('td')[3].text()).toBe('—')
    expect(rows[1].findAll('td')[3].text()).not.toBe('—')
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

  it('removes the node through the typed confirmation', async () => {
    const store = useConfigStore()
    store.draft = config({ interfaces: [...config().interfaces, node()] })
    store.loaded = true
    const wrapper = mount(SettingsTab, { global: { stubs } })

    const confirm = wrapper.findComponent(ConfirmButton)
    expect(confirm.props('typed')).toBe('tailscale0')
    confirm.vm.$emit('confirm')
    await flushPromises()
    expect(store.tailscale).toBeNull()
    expect(store.interfaces.map((i) => i.name)).toEqual(['eth1'])
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
