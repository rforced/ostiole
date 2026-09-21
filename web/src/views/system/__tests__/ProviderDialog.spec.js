import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import ProviderDialog from '@/views/system/certificates/ProviderDialog.vue'

const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template: '<div v-if="open"><slot /></div>',
}

const KINDS = [
  {
    kind: 'cloudflare',
    label: 'Cloudflare',
    fields: [
      { key: 'token', label: 'API token', secret: true, required: true },
      { key: 'zoneToken', label: 'Zone token', secret: true },
    ],
  },
  {
    kind: 'exec',
    label: 'Program',
    fields: [
      { key: 'program', label: 'Program', required: true },
      { key: 'mode', label: 'Mode', hint: 'Empty, or RAW to receive the record instead.' },
    ],
  },
]

function open(provider = null) {
  setActivePinia(createPinia())
  const config = useConfigStore()
  config.replaceDraft({ version: 6, zones: [], interfaces: [], rules: [], nat: {}, system: {} })
  const wrapper = mount(ProviderDialog, {
    props: { open: true, provider, kinds: KINDS },
    global: { stubs: { AppDialog: AppDialogStub } },
  })
  return { wrapper, config }
}

const save = (wrapper) => wrapper.findAll('button').find((b) => b.text() === 'Save to draft')

describe('ProviderDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('builds its fields from the kind, and hides the secret ones', async () => {
    const { wrapper } = open()
    expect(wrapper.get('#prov-token').attributes('type')).toBe('password')
    expect(wrapper.find('#prov-program').exists()).toBe(false)

    await wrapper.get('#prov-kind').setValue('exec')
    await flushPromises()
    expect(wrapper.find('#prov-token').exists()).toBe(false)
    expect(wrapper.get('#prov-program').attributes('type')).toBe('text')
  })

  it('will not save without the required fields', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#prov-id').setValue('cf')
    expect(save(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.get('#prov-token').setValue('secret')
    await wrapper.get('#prov-wait').setValue(90)
    expect(save(wrapper).attributes('disabled')).toBeUndefined()

    await wrapper.get('form').trigger('submit')
    expect(config.dnsProviders[0]).toEqual({
      id: 'cf',
      kind: 'cloudflare',
      settings: { token: 'secret' },
      propagationSeconds: 90,
    })
  })

  it('drops the settings of a kind it is no longer', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#prov-id').setValue('mixed')
    await wrapper.get('#prov-token').setValue('secret')
    await wrapper.get('#prov-kind').setValue('exec')
    await flushPromises()
    await wrapper.get('#prov-program').setValue('/bin/true')
    await wrapper.get('form').trigger('submit')

    expect(config.dnsProviders[0].settings).toEqual({ program: '/bin/true' })
  })
})
