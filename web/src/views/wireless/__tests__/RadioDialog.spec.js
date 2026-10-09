import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import RadioDialog from '@/views/wireless/RadioDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

function open(radio, card = null) {
  const config = useConfigStore()
  const cfg = {
    version: 12,
    zones: [],
    interfaces: [],
    rules: [],
    wireless: { country: 'US', radios: [radio] },
  }
  config.saved = cfg
  config.replaceDraft(cfg)
  const wrapper = mount(RadioDialog, {
    props: { open: true, radio: config.radios[0], card },
    global: { stubs },
  })
  return { wrapper, config }
}

describe('RadioDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens a radio on auto channel at full power as saved', async () => {
    const { wrapper, config } = open({
      name: 'wlp3s0',
      enabled: true,
      band: '5g',
      width: 80,
      standard: 'ax',
    })
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('keeps a channel while the card has not answered', async () => {
    const { wrapper, config } = open({
      name: 'wlp3s0',
      enabled: true,
      band: '2g',
      channel: 6,
      width: 20,
      standard: 'n',
    })
    await wrapper.get('form').trigger('submit')
    expect(config.radios[0].channel).toBe(6)
    expect(config.dirty).toBe(false)
  })
})
