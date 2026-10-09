import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import SettingsTab from '@/views/vpn/tailscale/SettingsTab.vue'

describe('Tailscale SettingsTab', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('leaves a switch turned on and off again out of the draft', async () => {
    const config = useConfigStore()
    const cfg = {
      version: 12,
      zones: [{ name: 'lan' }, { name: 'tailnet' }],
      interfaces: [
        {
          name: 'tailscale0',
          zone: 'tailnet',
          enabled: true,
          ipv4: { mode: 'none' },
          ipv6: { mode: 'none' },
          tailscale: { port: 41641 },
        },
      ],
      rules: [],
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(SettingsTab, { global: { stubs: { RouterLink: true } } })
    const label = wrapper
      .findAll('label')
      .find((l) => l.text().startsWith('Advertise as exit node'))
    const input = wrapper.get(`#${label.attributes('for')}`)
    await input.setValue(true)
    expect(config.dirty).toBe(true)
    await input.setValue(false)
    expect(config.dirty).toBe(false)
  })
})
