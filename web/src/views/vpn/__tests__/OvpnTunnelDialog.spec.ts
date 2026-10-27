import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { Interface } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'
import { configOf, must, type DeepPartial } from '@/test/fixtures'
import OvpnTunnelDialog from '@/views/vpn/openvpn/OvpnTunnelDialog.vue'

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
  OvpnFileDialog: true,
}

const TUNNEL = {
  name: 'ovpn0',
  zone: 'vpnout',
  enabled: true,
  ipv4: { mode: 'none' },
  ipv6: { mode: 'none' },
  openvpn: {
    remotes: [
      { host: 'vpn.example', port: 1194, proto: 'udp' },
      { host: 'lab-server', port: 443, proto: 'tcp' },
    ],
    caPem: '-----BEGIN CERTIFICATE-----\nTUFERS1VUA==\n-----END CERTIFICATE-----\n',
    username: 'acme',
    password: 'not-a-real-password',
    routes: ['10.8.0.0/24'],
    keepalive: 10,
  },
} satisfies DeepPartial<Interface>

async function open() {
  const config = useConfigStore()
  const cfg = configOf({
    version: 12,
    zones: [
      { name: 'wan', external: true },
      { name: 'vpnout', external: true },
    ],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true, ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } },
      TUNNEL,
    ],
    rules: [],
  })
  config.saved = JSON.parse(JSON.stringify(cfg)) as typeof cfg
  config.replaceDraft(JSON.parse(JSON.stringify(cfg)) as typeof cfg)
  const tunnel = must(config.interfaces.find((i) => i.name === 'ovpn0'))
  const wrapper = mount(OvpnTunnelDialog, {
    props: { open: false, tunnel },
    global: { stubs },
  })
  await wrapper.setProps({ open: true })
  return { wrapper, config }
}

const saved = (config: ReturnType<typeof useConfigStore>) =>
  must(config.interfaces.find((i) => i.name === 'ovpn0'))

describe('OvpnTunnelDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('leaves the draft clean when nothing is touched', async () => {
    const { wrapper, config } = await open()
    expect(wrapper.find<HTMLTextAreaElement>('#ovt-remotes').element.value).toBe(
      'vpn.example 1194 udp\nlab-server 443 tcp',
    )
    expect(wrapper.find<HTMLInputElement>('#ovt-routes').element.value).toBe('10.8.0.0/24')
    expect(wrapper.find<HTMLInputElement>('#ovt-password').element.value).toBe('')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(config.dirty).toBe(false)
    expect(saved(config)).toEqual(TUNNEL)
  })

  it('saves remotes and routes as typed, and keeps the saved password', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#ovt-remotes').setValue('lab-server 1195 tcp\nvpn.example 1194 udp\n')
    await wrapper.find('#ovt-routes').setValue('10.8.0.0/24, 10.9.0.0/16')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(saved(config).openvpn).toEqual({
      ...TUNNEL.openvpn,
      remotes: [
        { host: 'lab-server', port: 1195, proto: 'tcp' },
        { host: 'vpn.example', port: 1194 },
      ],
      routes: ['10.8.0.0/24', '10.9.0.0/16'],
    })
  })

  it('clears the password only on purpose', async () => {
    const { wrapper, config } = await open()
    const clear = must(wrapper.findAll('button').find((b) => b.text() === 'Clear'))
    await clear.trigger('click')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(must(saved(config).openvpn).password).toBeUndefined()
    expect(must(saved(config).openvpn).username).toBe('acme')
  })

  it('says which remote line will not do and saves nothing', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#ovt-remotes').setValue('vpn.example 1194 udp\nlab-server tcp')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('Line 2 of the remotes must be a host, a port and udp or tcp.')
    expect(config.dirty).toBe(false)
  })
})
