import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { Session } from '@/lib/api'
import type { Interface } from '@/lib/model.gen'
import type { Config } from '@/lib/model.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import InterfaceDialog from '@/views/interfaces/InterfaceDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
  RouterLink: true,
}

function open(iface, links = []) {
  const config = useConfigStore()
  config.replaceDraft({ version: 3, zones: [{ name: 'lan' }], interfaces: [], rules: [] } as Config)
  const wrapper = mount(InterfaceDialog, {
    props: { open: true, iface, links },
    global: { stubs },
  })
  return { wrapper, config, mtu: wrapper.get<HTMLInputElement>('#if-mtu') }
}

const iface = (over: Partial<Interface> = {}): Interface => ({
  name: 'eth1',
  enabled: true,
  ipv4: { mode: 'none' },
  ipv6: { mode: 'none' },
  ...over,
})

describe('InterfaceDialog MTU', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('shows what the link is running at rather than 0', () => {
    const { mtu } = open(iface(), [{ name: 'eth1', kind: 'ethernet', mtu: 1500 }])
    expect(mtu.element.value).toBe('1500')
  })

  it('falls back to the Ethernet default when the link is not present yet', () => {
    const { mtu } = open(iface(), [])
    expect(mtu.element.value).toBe('1500')
  })

  it('keeps a configured MTU over the live one', () => {
    const { mtu } = open(iface({ mtu: 9000 }), [{ name: 'eth1', mtu: 1500 }])
    expect(mtu.element.value).toBe('9000')
  })

  // Saving the default has to leave the configuration alone: an explicit
  // 1500 and no MTU at all reach the kernel the same way, and the shorter
  // one is what every other interface already looks like.
  it('stores nothing when the MTU is left at the default', async () => {
    const { wrapper, config } = open(iface(), [{ name: 'eth1', mtu: 1500 }])
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth1').mtu).toBeUndefined()
  })

  it('stores a value that differs from the default', async () => {
    const { wrapper, config, mtu } = open(iface(), [{ name: 'eth1', mtu: 1500 }])
    await mtu.setValue(9000)
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth1').mtu).toBe(9000)
  })

  // A link already running a jumbo MTU nobody configured loses it on the
  // next reboot. The field is pre-filled with it, so saving pins it.
  it('pins an MTU the router is running but the configuration does not ask for', async () => {
    const { wrapper, config } = open(iface(), [{ name: 'eth1', mtu: 9000 }])
    expect(wrapper.text()).toContain('is running at 9000')
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth1').mtu).toBe(9000)
  })

  it('takes the default of a VLAN from its parent', () => {
    const { mtu, wrapper } = open(iface({ name: 'eth1.10', vlan: { parent: 'eth1', id: 10 } }), [
      { name: 'eth1', mtu: 9000 },
      { name: 'eth1.10', mtu: 9000 },
    ])
    expect(mtu.element.value).toBe('9000')
    expect(wrapper.text()).toContain('inherited from eth1')
    // 9000 is the parent's MTU, so it is the default here rather than
    // drift worth warning about.
    expect(wrapper.text()).not.toContain('is running at')
  })
})

// Switching an interface on or off in an anti-lockout zone changes where
// the router is managed from, which is the admin's call.
describe('InterfaceDialog anti-lockout', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function enabled(role, zone) {
    useAuthStore().user = { username: role, role } as Session
    useConfigStore().replaceDraft({
      version: 12,
      zones: [{ name: 'lan', antiLockout: true }, { name: 'dmz' }],
      interfaces: [iface({ zone })],
      rules: [],
    } as Config)
    const wrapper = mount(InterfaceDialog, {
      props: { open: true, iface: iface({ zone }) },
      global: { stubs },
    })
    return wrapper
      .findAll('input[type="checkbox"]')
      .find((i) => i.element.parentElement.textContent.trim().startsWith('Enabled'))
  }

  it('keeps it from an operator', () => {
    expect(enabled('operator', 'lan').attributes('disabled')).toBeDefined()
  })

  it('leaves it to an operator in any other zone, and to an admin', () => {
    expect(enabled('operator', 'dmz').attributes('disabled')).toBeUndefined()
    expect(enabled('admin', 'lan').attributes('disabled')).toBeUndefined()
  })
})

describe('InterfaceDialog round trip', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps a configured MTU that equals the default', async () => {
    const config = useConfigStore()
    const cfg = {
      version: 3,
      zones: [{ name: 'lan' }],
      interfaces: [iface({ zone: 'lan', mtu: 1500 })],
      rules: [],
    } as Config
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(InterfaceDialog, {
      props: {
        open: true,
        iface: config.findInterface('eth1'),
        links: [{ name: 'eth1', mtu: 1500 }],
      },
      global: { stubs },
    })
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth1').mtu).toBe(1500)
    expect(config.dirty).toBe(false)
  })
})

describe('InterfaceDialog vendor class', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // eth1 is in the saved configuration, so submitting is a change to it.
  function edit(ipv4) {
    const config = useConfigStore()
    const cfg = {
      version: 3,
      zones: [{ name: 'lan' }],
      interfaces: [iface({ zone: 'lan', ipv4 })],
      rules: [],
    } as Config
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(InterfaceDialog, {
      props: { open: true, iface: config.findInterface('eth1') },
      global: { stubs },
    })
    return { wrapper, config }
  }

  it('is offered on a DHCP interface', () => {
    const { wrapper } = edit({ mode: 'dhcp' })
    expect(wrapper.get('label[for="if-v4-vendor"]').text()).toBe('Vendor class')
    expect(wrapper.get<HTMLInputElement>('#if-v4-vendor').element.value).toBe('')
  })

  it.each(['none', 'static'])('is not offered in %s mode', (mode) => {
    const { wrapper } = edit({ mode })
    expect(wrapper.find('#if-v4-vendor').exists()).toBe(false)
  })

  it('is written trimmed', async () => {
    const { wrapper, config } = edit({ mode: 'dhcp' })
    await wrapper.get<HTMLInputElement>('#if-v4-vendor').setValue('  acme-router ')
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth1').ipv4.vendorClass).toBe('acme-router')
  })

  it.each(['', '   '])('is dropped when the field holds %j', async (value) => {
    const { wrapper, config } = edit({ mode: 'dhcp', vendorClass: 'acme-router' })
    await wrapper.get<HTMLInputElement>('#if-v4-vendor').setValue(value)
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth1').ipv4).not.toHaveProperty('vendorClass')
  })

  it('is dropped when the interface stops being a DHCP client', async () => {
    const { wrapper, config } = edit({ mode: 'dhcp', vendorClass: 'acme-router' })
    await wrapper.get('#if-v4-mode').setValue('static')
    await wrapper.get('#if-v4-addr').setValue('192.0.2.1/24')
    await wrapper.get('form').trigger('submit')
    const { ipv4 } = config.findInterface('eth1')
    expect(ipv4.mode).toBe('static')
    expect(ipv4).not.toHaveProperty('vendorClass')
  })

  it('shows a saved one and leaves the draft clean when nothing is touched', async () => {
    const { wrapper, config } = edit({ mode: 'dhcp', vendorClass: 'acme-router' })
    expect(wrapper.get<HTMLInputElement>('#if-v4-vendor').element.value).toBe('acme-router')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('adds none to a DHCP interface without one when nothing is touched', async () => {
    const { wrapper, config } = edit({ mode: 'dhcp' })
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
