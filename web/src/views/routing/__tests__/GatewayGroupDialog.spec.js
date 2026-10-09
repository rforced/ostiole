import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import GatewayGroupDialog from '@/views/routing/GatewayGroupDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

describe('GatewayGroupDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens a group as saved and leaves an untouched save alone', async () => {
    const config = useConfigStore()
    const line = (name) => ({
      name,
      zone: 'wan',
      enabled: true,
      ipv4: { mode: 'dhcp' },
      ipv6: { mode: 'none' },
    })
    const cfg = {
      version: 12,
      zones: [{ name: 'wan', external: true }],
      interfaces: [line('eth0'), line('wwan0')],
      rules: [],
      gateways: [
        { name: 'wan', enabled: true, interface: 'eth0' },
        { name: 'lte', enabled: true, interface: 'wwan0', priority: 1 },
      ],
      gatewayGroups: [
        {
          name: 'failover',
          enabled: true,
          members: [{ gateway: 'wan' }, { gateway: 'lte', tier: 1 }],
          onDown: 'fallback',
        },
      ],
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(GatewayGroupDialog, {
      props: { open: true, group: config.gatewayGroups[0] },
      global: { stubs },
    })
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
