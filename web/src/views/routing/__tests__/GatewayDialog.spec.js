import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import GatewayDialog from '@/views/routing/GatewayDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template:
      '<div><p class="description">{{ description }}</p><slot /><slot name="footer" /></div>',
  },
}

function open(gateway = null, more = {}) {
  const config = useConfigStore()
  config.replaceDraft({
    version: 12,
    ...more,
    zones: [
      { name: 'wan', external: true },
      { name: 'vpn', external: true },
    ],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true, ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } },
      {
        name: 'wg1',
        zone: 'vpn',
        enabled: true,
        ipv4: { mode: 'static', address: '10.66.1.2/32' },
        ipv6: { mode: 'none' },
        wireguard: { privateKey: 'x', peers: [] },
      },
    ],
    gateways: gateway ? [gateway] : [{ name: 'wan', enabled: true, interface: 'eth0' }],
    rules: [],
  })
  const wrapper = mount(GatewayDialog, {
    props: { open: true, gateway: gateway ? config.gateways[0] : null },
    global: { stubs },
  })
  return { wrapper, config }
}

describe('GatewayDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('asks a line for its priority', async () => {
    const { wrapper } = open()
    await flushPromises()
    expect(wrapper.find('#gw-if').element.value).toBe('eth0')
    expect(wrapper.find('#gw-prio').exists()).toBe(true)
    expect(wrapper.find('#gw-monitor').attributes('required')).toBeUndefined()
  })

  // No address on a tunnel sends rule traffic into it: it never carries the
  // default route, so there is no priority to give, and nothing to ping but
  // a monitor beyond the tunnel.
  it('makes a tunnel with no address a tunnel gateway', async () => {
    const { wrapper, config } = open()
    await wrapper.find('#gw-if').setValue('wg1')
    expect(wrapper.find('#gw-prio').exists()).toBe(false)
    expect(wrapper.find('#gw-monitor').attributes('required')).toBeDefined()
    expect(wrapper.find('.description').text()).toBe(
      'With the tunnel down, its traffic is dropped.',
    )
    expect(wrapper.text()).toContain('Empty sends traffic from rules into the tunnel.')

    await wrapper.find('#gw-name').setValue('vpn')
    await wrapper.find('#gw-monitor').setValue('10.64.0.1')
    await wrapper.find('form').trigger('submit')
    expect(config.gateways.find((g) => g.name === 'vpn')).toEqual({
      name: 'vpn',
      enabled: true,
      interface: 'wg1',
      priority: 0,
      monitor: '10.64.0.1',
    })
  })

  it('treats a tunnel given an address as a line', async () => {
    const { wrapper } = open()
    await wrapper.find('#gw-if').setValue('wg1')
    await wrapper.find('#gw-addr').setValue('10.66.1.1')
    expect(wrapper.find('#gw-prio').exists()).toBe(true)
    expect(wrapper.find('#gw-monitor').attributes('required')).toBeUndefined()
  })

  // Empty is the next hop, said where the address would go; the one
  // address offered is the DNS upstream, in a family the gateway probes.
  it('says what an empty monitor probes, and offers the DNS upstream', async () => {
    const { wrapper } = open(null, {
      services: { dns: { enabled: true, upstreams: ['2001:db8::53', '192.0.2.53'] } },
    })
    await flushPromises()
    const monitor = wrapper.find('#gw-monitor')
    expect(monitor.attributes('placeholder')).toBe('the next hop')
    expect(wrapper.text()).toContain('Empty probes the next hop, which says the line is up.')
    const use = () => wrapper.findAll('button').find((b) => b.text().startsWith('Use '))
    expect(use().text()).toBe('Use 2001:db8::53, the DNS upstream')
    await wrapper.find('#gw-addr').setValue('198.51.100.1')
    expect(use().text()).toBe('Use 192.0.2.53, the DNS upstream')
    await use().trigger('click')
    expect(monitor.element.value).toBe('192.0.2.53')
    expect(use()).toBeUndefined()
  })

  it('offers no upstream to a router that looks names up itself', async () => {
    const { wrapper } = open(null, {
      services: { dns: { enabled: true, resolver: 'recursive', upstreams: ['192.0.2.53'] } },
    })
    await flushPromises()
    expect(wrapper.findAll('button').some((b) => b.text().startsWith('Use '))).toBe(false)
  })

  it('offers the first DNS over TLS resolver, and an IPv4 one to a tunnel', async () => {
    const { wrapper } = open(null, {
      services: {
        dns: {
          enabled: true,
          resolver: 'tls',
          tlsUpstreams: [
            { address: '2001:db8::9', hostname: 'dns.example' },
            { address: '192.0.2.9', hostname: 'dns.example' },
          ],
        },
      },
    })
    await flushPromises()
    const use = () => wrapper.findAll('button').find((b) => b.text().startsWith('Use '))
    expect(use().text()).toBe('Use 2001:db8::9, the DNS upstream')
    await wrapper.find('#gw-if').setValue('wg1')
    expect(use().text()).toBe('Use 192.0.2.9, the DNS upstream')
  })

  // Empty keeps the default, which the placeholder shows; 0 is off and is
  // kept as such.
  it('keeps the thresholds a gateway warns over, unset unless given', async () => {
    const { wrapper, config } = open({
      name: 'wan',
      enabled: true,
      interface: 'eth0',
      slowAboveMs: 0,
    })
    await flushPromises()
    expect(wrapper.find('#gw-slow').element.value).toBe('0')
    expect(wrapper.find('#gw-lossy').attributes('placeholder')).toBe('10')
    await wrapper.find('form').trigger('submit')
    expect(config.gateways[0].slowAboveMs).toBe(0)
    expect('lossyAbovePercent' in config.gateways[0]).toBe(false)

    const again = open({ name: 'wan', enabled: true, interface: 'eth0' })
    await flushPromises()
    await again.wrapper.find('#gw-lossy').setValue('5')
    await again.wrapper.find('form').trigger('submit')
    expect(again.config.gateways[0].lossyAbovePercent).toBe(5)
    expect('slowAboveMs' in again.config.gateways[0]).toBe(false)
  })
})
