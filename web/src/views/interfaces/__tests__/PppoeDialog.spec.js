import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import PppoeDialog from '@/views/interfaces/PppoeDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

describe('PppoeDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('shows the line settings in place, with no fold to open', () => {
    useConfigStore().replaceDraft({ version: 12, zones: [], interfaces: [], rules: [] })
    const wrapper = mount(PppoeDialog, {
      props: { open: true, candidates: [{ name: 'eth1', kind: 'ethernet' }] },
      global: { stubs },
    })
    expect(wrapper.get('legend').text()).toBe('Line')
    for (const id of ['ppp-service', 'ppp-ac', 'ppp-mtu', 'ppp-lcp', 'ppp-fail']) {
      expect(wrapper.find(`#${id}`).exists()).toBe(true)
    }
    expect(wrapper.text()).not.toContain('Advanced')
  })

  it('reopens a session as saved and leaves an untouched save alone', async () => {
    const config = useConfigStore()
    const cfg = {
      version: 12,
      zones: [{ name: 'wan', external: true }],
      rules: [],
      interfaces: [
        { name: 'eth1', enabled: true, ipv4: { mode: 'none' }, ipv6: { mode: 'none' } },
        {
          name: 'ppp0',
          zone: 'wan',
          enabled: true,
          ipv4: { mode: 'ppp' },
          ipv6: { mode: 'none' },
          pppoe: { parent: 'eth1', username: 'dsl-0421@example.net', password: 'not-a-real-one' },
        },
      ],
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(PppoeDialog, {
      props: {
        open: true,
        candidates: [{ name: 'eth1', kind: 'ethernet' }],
        iface: config.findInterface('ppp0'),
      },
      global: { stubs },
    })
    for (const id of ['ppp-mtu', 'ppp-lcp', 'ppp-fail']) {
      expect(wrapper.get(`#${id}`).element.value).toBe('')
    }
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
