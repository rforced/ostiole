import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import ServerDialog from '@/views/services/dhcp/ServerDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

describe('DHCP ServerDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens a pool as saved and leaves an untouched save alone', async () => {
    const config = useConfigStore()
    const cfg = {
      version: 12,
      zones: [{ name: 'lan' }],
      interfaces: [
        {
          name: 'eth1',
          zone: 'lan',
          enabled: true,
          ipv4: { mode: 'static', address: '192.0.2.1/24' },
          ipv6: { mode: 'none' },
        },
      ],
      rules: [],
      services: {
        dhcp: {
          enabled: true,
          servers: [
            {
              interface: 'eth1',
              enabled: true,
              rangeStart: '192.0.2.100',
              rangeEnd: '192.0.2.199',
            },
          ],
        },
        dns: { enabled: false },
      },
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(ServerDialog, {
      props: { open: true, server: config.draft.services.dhcp.servers[0] },
      global: { stubs },
    })
    expect(wrapper.get('#sc-lease').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
