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
    expect(wrapper.get('#radio-channel').element.value).toBe('6')
    await wrapper.get('form').trigger('submit')
    expect(config.radios[0].channel).toBe(6)
    expect(config.dirty).toBe(false)
  })

  it('shows a channel the card does not offer until another is picked', async () => {
    const card = {
      bands: {
        '2g': {
          channels: [
            { number: 1, mhz: 2412 },
            { number: 11, mhz: 2462 },
          ],
        },
      },
    }
    const { wrapper, config } = open(
      { name: 'wlp3s0', enabled: true, band: '2g', channel: 6, width: 20, standard: 'n' },
      card,
    )
    const select = wrapper.get('#radio-channel')
    expect(select.element.value).toBe('6')
    await select.setValue('11')
    expect(select.find('option[value="6"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(config.radios[0].channel).toBe(11)
  })
})
