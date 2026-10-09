import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import ProviderDialog from '@/views/system/providers/ProviderDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

describe('ProviderDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps the settings of a provider whose kind has not loaded', async () => {
    const config = useConfigStore()
    const cfg = {
      version: 12,
      zones: [],
      interfaces: [],
      rules: [],
      dnsProviders: [{ id: 'cf', kind: 'cloudflare', settings: { token: 'not-a-real-token' } }],
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(ProviderDialog, {
      props: { open: true, provider: config.dnsProviders[0], kinds: [] },
      global: { stubs },
    })
    await wrapper.get('form').trigger('submit')
    expect(config.dnsProviders[0].settings).toEqual({ token: 'not-a-real-token' })
    expect(config.dirty).toBe(false)
  })
})
