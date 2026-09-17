import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import InterfaceDialog from '@/views/interfaces/InterfaceDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
  RouterLink: true,
}

function open(iface, links = []) {
  const config = useConfigStore()
  config.replaceDraft({ version: 2, zones: [{ name: 'lan' }], interfaces: [], rules: [] })
  const wrapper = mount(InterfaceDialog, {
    props: { open: true, iface, links },
    global: { stubs },
  })
  return { wrapper, config, mtu: wrapper.get('#if-mtu') }
}

const iface = (over = {}) => ({
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
