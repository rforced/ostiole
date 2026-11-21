import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import { emptyConfig, iface, must } from '@/test/fixtures'
import SettingsTab from '@/views/vpn/tailscale/SettingsTab.vue'

describe('Tailscale SettingsTab', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('leaves a switch turned on and off again out of the draft', async () => {
    const config = useConfigStore()
    const cfg = {
      ...emptyConfig(),
      version: 12,
      zones: [{ name: 'lan' }, { name: 'tailnet' }],
      interfaces: [
        iface({
          name: 'tailscale0',
          zone: 'tailnet',
          enabled: true,
          ipv4: { mode: 'none' },
          ipv6: { mode: 'none' },
          tailscale: { port: 41641 },
        }),
      ],
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(SettingsTab, { global: { stubs: { RouterLink: true } } })
    const label = must(
      wrapper.findAll('label').find((l) => l.text().startsWith('Advertise as exit node')),
    )
    const input = wrapper.get(`#${label.attributes('for')}`)
    await input.setValue(true)
    expect(config.dirty).toBe(true)
    await input.setValue(false)
    expect(config.dirty).toBe(false)
  })

  it('hints when an enabled exit-node gateway prevents advertising as an exit node', () => {
    const config = useConfigStore()
    const cfg = {
      ...emptyConfig(),
      version: 16,
      zones: [{ name: 'lan' }, { name: 'tailnet' }],
      interfaces: [
        iface({
          name: 'tailscale0',
          zone: 'tailnet',
          enabled: true,
          ipv4: { mode: 'none' },
          ipv6: { mode: 'none' },
          tailscale: { port: 41641 },
        }),
      ],
      gateways: [{ name: 'amsterdam', enabled: true, interface: 'tailscale0', exitNode: 'ams-1' }],
    }
    config.replaceDraft(cfg)
    const wrapper = mount(SettingsTab, { global: { stubs: { RouterLink: true } } })
    expect(wrapper.text()).toContain(
      'Cannot advertise an exit node while gateway amsterdam uses one.',
    )
  })
})
