import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import V6ServerDialog from '@/views/services/dhcp/V6ServerDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

describe('DHCP V6ServerDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens an advertisement as saved and leaves an untouched save alone', async () => {
    const config = useConfigStore()
    const cfg = {
      version: 12,
      zones: [{ name: 'lan' }],
      interfaces: [
        {
          name: 'eth1',
          zone: 'lan',
          enabled: true,
          ipv4: { mode: 'none' },
          ipv6: { mode: 'static', address: '2001:db8:1::1/64' },
        },
      ],
      rules: [],
      services: {
        dhcp: { enabled: true, v6: [{ interface: 'eth1', enabled: true, mode: 'slaac' }] },
        dns: { enabled: false },
      },
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(V6ServerDialog, {
      props: { open: true, server: config.draft.services.dhcp.v6[0] },
      global: { stubs },
    })
    expect(wrapper.get('#v6-lease').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
