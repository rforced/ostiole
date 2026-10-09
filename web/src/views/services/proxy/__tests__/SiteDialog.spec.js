import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import SiteDialog from '@/views/services/proxy/SiteDialog.vue'

const site = {
  id: 'shop',
  enabled: true,
  hosts: ['shop.example.test'],
  pool: 'web',
  allowFrom: ['office', '192.0.2.0/24'],
}

function open() {
  const cfg = {
    version: 11,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [],
    rules: [],
    aliases: [{ name: 'office', type: 'hosts', entries: ['198.51.100.0/24'] }],
    services: {
      dhcp: { enabled: false },
      dns: { enabled: false },
      proxy: {
        enabled: true,
        pools: [{ id: 'web', upstreams: [{ address: '192.168.1.20:80' }] }],
        sites: [structuredClone(site)],
      },
    },
  }
  const config = useConfigStore()
  config.saved = structuredClone(cfg)
  config.replaceDraft(cfg)
  config.loaded = true
  const wrapper = mount(SiteDialog, {
    props: { open: true, site: config.draft.services.proxy.sites[0] },
    global: { stubs: { AppDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
  })
  return { wrapper, config }
}

describe('SiteDialog untouched save', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps the order of the allow list', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    expect(wrapper.get('#site-allow').element.value).toBe('192.0.2.0/24')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('writes an allow list the user changes', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    await wrapper.get('#site-allow').setValue('192.0.2.0/24\n203.0.113.0/24')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(true)
    expect(config.draft.services.proxy.sites[0].allowFrom).toEqual([
      '192.0.2.0/24',
      '203.0.113.0/24',
      'office',
    ])
  })
})
