import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import GatewayDialog from '@/views/routing/GatewayDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><p class="description">{{ description }}</p><slot /></div>',
  },
}

function open(gateway = null) {
  const config = useConfigStore()
  config.replaceDraft({
    version: 12,
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
})
