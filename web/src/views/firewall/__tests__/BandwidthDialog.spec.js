import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import BandwidthDialog from '@/views/firewall/shaping/BandwidthDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
}

function draft() {
  return {
    version: 3,
    zones: [{ name: 'lan' }, { name: 'wan', external: true }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true, ipv4: { mode: 'dhcp' } },
      { name: 'eth1', zone: 'lan', enabled: true, ipv4: { mode: 'static' } },
      { name: 'eth2', enabled: false, ipv4: { mode: 'none' } },
      { name: 'eth3', enabled: true, ipv4: { mode: 'none' } },
      { name: 'br0', enabled: true, bridge: { members: ['eth3'] }, ipv4: { mode: 'none' } },
    ],
    rules: [],
  }
}

function open(iface = null, over = {}) {
  const config = useConfigStore()
  config.replaceDraft({ ...draft(), ...over })
  const wrapper = mount(BandwidthDialog, { props: { open: true, iface }, global: { stubs } })
  return { wrapper, config }
}

describe('BandwidthDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // The figure is stored in bit/s and typed in whatever unit the line was
  // sold in, so the two have to agree in both directions.
  it('stores the number and the unit as bit per second', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#bw-if').setValue('eth0')
    await wrapper.get('#bw-down').setValue('200')
    await wrapper.get('#bw-up').setValue('20')
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth0').shaping).toEqual({
      download: 200_000_000,
      upload: 20_000_000,
    })
  })

  it('reads a stored figure back in the largest whole unit', () => {
    const iface = { name: 'eth0', shaping: { download: 1_000_000_000, upload: 512_000 } }
    const { wrapper } = open(iface, {
      ...draft(),
      interfaces: draft().interfaces.map((i) => (i.name === 'eth0' ? { ...i, ...iface } : i)),
    })
    expect(wrapper.get('#bw-down').element.value).toBe('1')
    expect(wrapper.get('#bw-up').element.value).toBe('512')
  })

  it('keeps a link type that is not the default and drops one that is', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#bw-if').setValue('eth0')
    await wrapper.get('#bw-down').setValue('50')
    await wrapper.get('#bw-link').setValue('docsis')
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth0').shaping).toEqual({ download: 50_000_000, link: 'docsis' })
  })

  // Neither direction given is not a configuration, it is an interface
  // listed as shaped with nothing shaped on it.
  it('will not save with both directions empty', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#bw-if').setValue('eth0')
    expect(wrapper.get('button[type=submit]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('eth0').shaping).toBeUndefined()
  })

  // A port carries its master's traffic, a disabled interface carries
  // none, and one that already has a speed is edited rather than added.
  it('offers only the interfaces a speed can go on', () => {
    const { wrapper } = open()
    const names = wrapper
      .get('#bw-if')
      .findAll('option')
      .map((o) => o.element.value)
      .filter(Boolean)
    expect(names).toEqual(['eth0', 'eth1', 'br0'])
  })

  it('pins the interface while editing an existing one', () => {
    const { wrapper } = open({ name: 'eth1', shaping: { download: 50_000_000 } })
    expect(wrapper.get('#bw-if').attributes('disabled')).toBeDefined()
  })

  // On an interface facing hosts the figure is a cap, not the line speed,
  // and the hint has to say which one it is asking for.
  it('changes the download hint for an internal interface', async () => {
    const { wrapper } = open()
    await wrapper.get('#bw-if').setValue('eth0')
    expect(wrapper.text()).toContain('what the line really delivers')
    await wrapper.get('#bw-if').setValue('eth1')
    expect(wrapper.text()).toContain('A cap for the hosts behind this interface.')
  })
})
