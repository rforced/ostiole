import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import PeerDialog from '@/views/vpn/PeerDialog.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal()
  return { ...mod, api: { ...mod.api, wireguard: { psk: vi.fn() } } }
})

const stubs = {
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
}
const FRIEND = 'TrMvSoP4jYQlY6RIzBgbssQqY3vxI2Pi+y71lOWWXX0='

function open(peer = null) {
  const config = useConfigStore()
  config.replaceDraft({
    version: 11,
    zones: [{ name: 'sites' }],
    interfaces: [
      {
        name: 'wg1',
        zone: 'sites',
        enabled: true,
        ipv4: { mode: 'static', address: '10.77.0.1/30' },
        ipv6: { mode: 'none' },
        wireguard: { peers: peer ? [peer] : [] },
      },
    ],
    rules: [],
  })
  const tunnel = config.findInterface('wg1')
  const wrapper = mount(PeerDialog, { props: { open: true, tunnel, peer }, global: { stubs } })
  return { wrapper, config }
}

const toggle = (wrapper, label) => wrapper.findAll('label').find((l) => l.text().startsWith(label))

describe('PeerDialog Translate', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('is off for a new peer and saved when turned on', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#pe-name').setValue('friend')
    await wrapper.get('#pe-pub').setValue(FRIEND)
    await wrapper.get('#pe-allowed').setValue('10.77.0.2/32, 192.168.50.0/24')
    const translate = toggle(wrapper, 'Translate to the tunnel address')
    const box = wrapper.get(`#${translate.attributes('for')}`)
    expect(box.element.checked).toBe(false)
    await box.setValue(true)
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('wg1').wireguard.peers[0].masquerade).toBe(true)
  })

  it('leaves the field out of a peer that does not translate', async () => {
    const peer = { name: 'friend', enabled: true, publicKey: FRIEND, allowedIps: ['10.77.0.2/32'] }
    const { wrapper, config } = open(peer)
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('wg1').wireguard.peers[0]).not.toHaveProperty('masquerade')
  })
})

describe('PeerDialog shown networks', () => {
  beforeEach(() => setActivePinia(createPinia()))

  const cabin = () => ({
    name: 'cabin',
    enabled: true,
    publicKey: FRIEND,
    allowedIps: ['10.77.0.2/32', '192.168.1.0/24'],
    theirs: [{ network: '192.168.1.0/24', as: '10.201.1.0/24' }],
    ours: [{ network: '192.168.1.0/24', as: '10.200.1.0/24' }],
  })

  it('keeps what a peer shows and drops the rows left empty', async () => {
    const { wrapper, config } = open(cabin())
    expect(wrapper.get('#pe-theirs-0').element.value).toBe('192.168.1.0/24')
    expect(wrapper.get('#pe-ours-as-0').element.value).toBe('10.200.1.0/24')
    await wrapper.get('#pe-theirs-as-0').setValue('10.202.1.0/24')
    const add = wrapper.findAll('button').filter((b) => b.text() === 'Add network')
    await add[0].trigger('click')
    expect(wrapper.find('#pe-theirs-1').exists()).toBe(true)
    await wrapper.get('form').trigger('submit')
    const saved = config.findInterface('wg1').wireguard.peers[0]
    expect(saved.theirs).toEqual([{ network: '192.168.1.0/24', as: '10.202.1.0/24' }])
    expect(saved.ours).toEqual([{ network: '192.168.1.0/24', as: '10.200.1.0/24' }])
  })

  it('leaves the fields out once every row is removed', async () => {
    const { wrapper, config } = open(cabin())
    await wrapper.get('[aria-label="Remove 192.168.1.0/24"]').trigger('click')
    await wrapper.get('[aria-label="Remove 192.168.1.0/24"]').trigger('click')
    await wrapper.get('form').trigger('submit')
    const saved = config.findInterface('wg1').wireguard.peers[0]
    expect(saved).not.toHaveProperty('theirs')
    expect(saved).not.toHaveProperty('ours')
  })
})

describe('PeerDialog device file', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  function openDevice(peer = null) {
    const config = useConfigStore()
    config.replaceDraft({
      version: 11,
      zones: [{ name: 'lan' }, { name: 'wg0' }, { name: 'wan', external: true }],
      interfaces: [
        {
          name: 'eth0',
          zone: 'wan',
          enabled: true,
          ipv4: { mode: 'dhcp' },
          ipv6: { mode: 'none' },
        },
        {
          name: 'eth1',
          zone: 'lan',
          enabled: true,
          ipv4: { mode: 'static', address: '192.168.1.1/24' },
          ipv6: { mode: 'none' },
        },
        {
          name: 'wg0',
          zone: 'wg0',
          enabled: true,
          ipv4: { mode: 'static', address: '10.66.0.1/24' },
          ipv6: { mode: 'static', address: 'fd66::1/64' },
          wireguard: {
            publicKey: 'ROUTER=',
            listenPort: 51820,
            peers: [
              { name: 'laptop', enabled: true, publicKey: FRIEND, allowedIps: ['10.66.0.2/32'] },
            ],
          },
        },
      ],
      rules: [],
      services: { dns: { enabled: true } },
    })
    api.wireguard.keys = vi
      .fn()
      .mockResolvedValue({ privateKey: 'DEVICE-PRIVATE=', publicKey: 'DEVICE=' })
    api.wireguard.psk.mockResolvedValue({ presharedKey: 'PSK=' })
    api.ddns = { status: vi.fn().mockResolvedValue({ records: [{ name: 'home.example.com' }] }) }
    api.overview = vi.fn().mockResolvedValue({
      interfaces: [
        { external: true, addresses: ['203.0.113.7/24', 'fe80::1/64'] },
        { external: false, addresses: ['192.168.1.1/24'] },
      ],
    })
    const tunnel = config.findInterface('wg0')
    const wrapper = mount(PeerDialog, {
      props: { open: true, tunnel, peer: peer && tunnel.wireguard.peers[0] },
      global: { stubs },
    })
    return { wrapper, config }
  }

  // Nothing after the pair carries the private key: not the draft, and no
  // request the dialog makes.
  it('makes the keys here and keeps only the public one', async () => {
    const { wrapper, config } = openDevice()
    expect(wrapper.find('#pe-pub').exists()).toBe(false)
    expect(wrapper.get('#pe-allowed').element.value).toBe('10.66.0.3/32, fd66::2/128')
    await wrapper.get('#pe-name').setValue('phone')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    const phone = config.findInterface('wg0').wireguard.peers.find((p) => p.name === 'phone')
    expect(phone).toMatchObject({ publicKey: 'DEVICE=', presharedKey: 'PSK=' })
    expect(JSON.stringify(config.draft)).not.toContain('DEVICE-PRIVATE=')
    for (const fn of [api.wireguard.keys, api.wireguard.psk, api.ddns.status, api.overview]) {
      expect(JSON.stringify(fn.mock.calls)).not.toContain('DEVICE-PRIVATE=')
    }

    const file = wrapper.get('pre').text()
    expect(file).toContain('PrivateKey = DEVICE-PRIVATE=')
    expect(file).toContain('Address = 10.66.0.3/32, fd66::2/128')
    expect(file).toContain('DNS = 10.66.0.1, fd66::1')
    expect(file).toContain('PublicKey = ROUTER=')
    expect(file).toContain('PresharedKey = PSK=')
    expect(file).toContain('AllowedIPs = 192.168.1.0/24, 10.66.0.0/24, fd66::/64')
    expect(file).toContain('Endpoint = home.example.com:51820')
    expect(file).not.toContain('PersistentKeepalive')
    expect(wrapper.find('svg[role="img"] path').attributes('d')).toMatch(/^M4 4h1v1h-1z/)

    await wrapper.get('#dv-route').setValue('everything')
    expect(wrapper.get('pre').text()).toContain('AllowedIPs = 0.0.0.0/0, ::/0')
    // The public address is offered after the name, and link-local never.
    const offered = wrapper.findAll('#dv-endpoints option').map((o) => o.attributes('value'))
    expect(offered).toEqual(['home.example.com', '203.0.113.7'])
  })

  // What the peer lists is what the router routes to it. The device holds
  // only its tunnel address, not the router's LAN typed in beside it.
  it('puts only the addresses the device holds in its file', async () => {
    const { wrapper } = openDevice()
    await wrapper.get('#pe-name').setValue('phone')
    await wrapper.get('#pe-allowed').setValue('10.66.0.3/32, 192.168.1.1/24')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('pre').text()).toMatch(/^Address = 10\.66\.0\.3\/32$/m)
  })

  it('makes no keys for a device with no address of its own', async () => {
    const { wrapper, config } = openDevice()
    await wrapper.get('#pe-name').setValue('phone')
    await wrapper.get('#pe-allowed').setValue('192.168.1.0/24')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(
      "Add the device's own address, such as 10.66.0.3/32.",
    )
    expect(api.wireguard.keys).not.toHaveBeenCalled()
    expect(config.findInterface('wg0').wireguard.peers.map((p) => p.name)).toEqual(['laptop'])
  })

  it('makes new keys for a peer only once the name is typed', async () => {
    const { wrapper, config } = openDevice(true)
    const confirm = useConfirmStore()
    const ask = vi.spyOn(confirm, 'ask').mockResolvedValue(true)
    const remake = wrapper.findAll('button').find((b) => b.text() === 'Make new keys')
    await remake.trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(expect.objectContaining({ typed: 'laptop' }))
    expect(config.findInterface('wg0').wireguard.peers[0].publicKey).toBe('DEVICE=')
    expect(wrapper.get('pre').text()).toContain('Address = 10.66.0.2/32')
  })
})
