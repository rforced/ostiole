import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import TunnelDialog from '@/views/vpn/TunnelDialog.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal()
  return {
    ...mod,
    api: { ...mod.api, wireguard: { keys: vi.fn(), psk: vi.fn(), publicKey: vi.fn() } },
  }
})

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

// The wg(8) man page's example private key and preshared key, and the
// public key of the first.
const EXAMPLE = 'yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk='
const OTHER = 'FpCyhws9cxwWoV4xELtfJvjJN+zQVRPISllRWgeopVE='
const EXAMPLE_PUBLIC = 'HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw='

const lan = {
  name: 'eth1',
  zone: 'lan',
  enabled: true,
  ipv4: { mode: 'static', address: '192.168.1.1/24' },
  ipv6: { mode: 'none' },
}

const wg0 = () => ({
  name: 'wg0',
  zone: 'wg0',
  enabled: true,
  logDrops: true,
  ipv4: { mode: 'static', address: '10.66.0.1/24' },
  ipv6: { mode: 'static', address: 'fd66::1/64' },
  wireguard: {
    privateKey: EXAMPLE,
    publicKey: EXAMPLE_PUBLIC,
    listenPort: 51820,
    peers: [{ name: 'laptop', enabled: true, publicKey: OTHER, allowedIps: ['10.66.0.2/32'] }],
  },
})

function open({ tunnel = null, zones = [{ name: 'lan' }] } = {}) {
  const config = useConfigStore()
  config.replaceDraft({
    version: 11,
    zones,
    interfaces: tunnel ? [lan, tunnel] : [lan],
    rules: [],
  })
  const wrapper = mount(TunnelDialog, {
    props: { open: true, tunnel: tunnel ? config.findInterface(tunnel.name) : null },
    global: { stubs },
  })
  return { wrapper, config }
}

describe('TunnelDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.wireguard.keys.mockResolvedValue({ privateKey: EXAMPLE, publicKey: EXAMPLE_PUBLIC })
  })

  // Every save used to write IPv6 none, wiping an address set through the
  // API; what the dialog does not show has to survive it as well.
  it('keeps the IPv6 address and what it does not show', async () => {
    const { wrapper, config } = open({ tunnel: wg0(), zones: [{ name: 'lan' }, { name: 'wg0' }] })
    expect(wrapper.get('#wg-v6').element.value).toBe('fd66::1/64')
    await wrapper.get('#wg-desc').setValue('Road warriors')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const saved = config.findInterface('wg0')
    expect(saved.ipv6).toEqual({ mode: 'static', address: 'fd66::1/64' })
    expect(saved.ipv4).toEqual({ mode: 'static', address: '10.66.0.1/24' })
    expect(saved.logDrops).toBe(true)
    expect(saved.description).toBe('Road warriors')
    expect(saved.wireguard.peers.map((p) => p.name)).toEqual(['laptop'])
    expect(api.wireguard.publicKey).not.toHaveBeenCalled()
  })

  it('asks the router for the public key of a pasted private key', async () => {
    api.wireguard.publicKey.mockResolvedValue({ publicKey: 'derived' })
    const { wrapper, config } = open({ tunnel: wg0(), zones: [{ name: 'lan' }, { name: 'wg0' }] })

    // Half a key asks nothing yet.
    await wrapper.get('#wg-priv').setValue(OTHER.slice(0, 20))
    expect(api.wireguard.publicKey).not.toHaveBeenCalled()
    expect(wrapper.get('#wg-pub').element.value).toBe('')

    await wrapper.get('#wg-priv').setValue(OTHER)
    await flushPromises()
    expect(api.wireguard.publicKey).toHaveBeenCalledWith(OTHER)
    expect(wrapper.get('#wg-pub').element.value).toBe('derived')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(config.findInterface('wg0').wireguard).toMatchObject({
      privateKey: OTHER,
      publicKey: 'derived',
    })
  })

  it('refuses to save a private key the router does not take', async () => {
    api.wireguard.publicKey.mockRejectedValue(
      new Error('not a WireGuard key: want 32 bytes, base64 encoded'),
    )
    const { wrapper, config } = open({ tunnel: wg0(), zones: [{ name: 'lan' }, { name: 'wg0' }] })
    await wrapper.get('#wg-priv').setValue('not a key')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('not a WireGuard key')
    expect(config.findInterface('wg0').wireguard.privateKey).toBe(EXAMPLE)
  })

  // A tunnel in lan inherited "Allow LAN to any" and anti-lockout, so every
  // peer reached everything, the web UI included.
  it('starts a new tunnel in a zone of its own', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    expect(wrapper.get('#wg-zone').element.value).toBe('+new')
    expect(wrapper.get('#wg-zone-new').element.value).toBe('wg0')

    // The name follows the interface until it is typed over.
    await wrapper.get('#wg-name').setValue('wg3')
    expect(wrapper.get('#wg-zone-new').element.value).toBe('wg3')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(config.zones.find((z) => z.name === 'wg3')).toEqual({ name: 'wg3' })
    expect(config.findInterface('wg3')).toMatchObject({
      zone: 'wg3',
      ipv4: { mode: 'static', address: '10.66.0.1/24' },
      ipv6: { mode: 'none' },
      wireguard: { privateKey: EXAMPLE, publicKey: EXAMPLE_PUBLIC, listenPort: 51820 },
    })
  })

  it('makes the new zone external when asked', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    await wrapper.get('#wg-zone-new').setValue('vpn')
    const external = wrapper
      .findAll('input[type="checkbox"]')
      .find((i) => i.element.parentElement.textContent.includes('External'))
    await external.setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(config.zones.find((z) => z.name === 'vpn')).toEqual({ name: 'vpn', external: true })
    expect(config.findInterface('wg0').zone).toBe('vpn')
  })

  it('takes the zone a deleted tunnel of the same name left behind', async () => {
    const { wrapper } = open({ zones: [{ name: 'lan' }, { name: 'wg0' }] })
    await flushPromises()
    expect(wrapper.get('#wg-zone').element.value).toBe('wg0')
    expect(wrapper.find('#wg-zone-new').exists()).toBe(false)
  })
})

describe('TunnelDialog round trip', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('reopens a tunnel with no peers as saved and leaves an untouched save alone', async () => {
    const tunnel = wg0()
    delete tunnel.wireguard.peers
    const { wrapper, config } = open({ tunnel, zones: [{ name: 'lan' }, { name: 'wg0' }] })
    config.saved = JSON.parse(JSON.stringify(config.draft))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(config.dirty).toBe(false)
  })
})
