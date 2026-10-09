import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ProviderDialog from '@/views/system/providers/ProviderDialog.vue'

vi.mock('@/lib/api', () => ({
  api: { dnsProviders: { test: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template: '<div v-if="open"><slot /><slot name="footer" /></div>',
}

const KINDS = [
  {
    kind: 'cloudflare',
    label: 'Cloudflare',
    dynamicDns: true,
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

function open(provider = null, role = 'admin') {
  setActivePinia(createPinia())
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  config.replaceDraft({ version: 11, zones: [], interfaces: [], rules: [], nat: {}, system: {} })
  const wrapper = mount(ProviderDialog, {
    props: { open: true, provider, kinds: KINDS },
    global: { stubs: { AppDialog: AppDialogStub } },
  })
  return { wrapper, config }
}

const save = (wrapper) => wrapper.findAll('button').find((b) => b.text() === 'Save to draft')

describe('ProviderDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('leaves an operator the domains and the wait, not the kind or credentials', async () => {
    const { wrapper } = open(
      { id: 'cf', kind: 'cloudflare', settings: { token: 'x' }, domains: ['example.com'] },
      'operator',
    )
    await flushPromises()
    expect(wrapper.get('#prov-kind').element.disabled).toBe(true)
    expect(wrapper.get('#prov-token').element.disabled).toBe(true)
    expect(wrapper.get('#prov-domains').element.disabled).toBe(false)
    expect(wrapper.get('#prov-wait').element.disabled).toBe(false)
    expect(wrapper.text()).toContain('Only an admin can change the kind and credentials.')
  })

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

  it('keeps the domains lower case, once each, and shows them again', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#prov-id').setValue('cf')
    await wrapper.get('#prov-token').setValue('secret')
    await wrapper.get('#prov-domains').setValue('Example.com\nhome.example.net, example.com')
    await wrapper.get('form').trigger('submit')
    expect(config.dnsProviders[0].domains).toEqual(['example.com', 'home.example.net'])

    const again = mount(ProviderDialog, {
      props: { open: true, provider: config.dnsProviders[0], kinds: KINDS },
      global: { stubs: { AppDialog: AppDialogStub } },
    })
    expect(again.get('#prov-domains').element.value).toBe('example.com\nhome.example.net')
  })

  it('leaves domains out when there are none', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#prov-id').setValue('cf')
    await wrapper.get('#prov-token').setValue('secret')
    await wrapper.get('form').trigger('submit')
    expect(config.dnsProviders[0]).not.toHaveProperty('domains')
  })

  it('tests the credentials as the form holds them, for a kind Ostiole can try', async () => {
    const { wrapper } = open()
    const button = (label) => wrapper.findAll('button').find((b) => b.text() === label)
    await wrapper.get('#prov-id').setValue('cf')
    await wrapper.get('#prov-token').setValue('secret')
    await wrapper.get('#prov-domains').setValue('example.com\nexample.org')
    api.dnsProviders.test.mockResolvedValue({
      zones: ['example.com', 'a.test', 'b.test', 'c.test'],
      more: 2,
      domains: [
        { domain: 'example.com' },
        {
          domain: 'example.org',
          error: 'Cloudflare has no zone example.org that this token can see',
        },
      ],
    })
    await button('Test').trigger('click')
    await flushPromises()
    expect(api.dnsProviders.test).toHaveBeenCalledWith({
      id: 'cf',
      kind: 'cloudflare',
      settings: { token: 'secret' },
      domains: ['example.com', 'example.org'],
    })
    const found = wrapper.get('[role="status"]').text().replaceAll('\u00a0', ' ')
    expect(found).toContain(
      'Cloudflare accepts the credentials. They see example.com, a.test, b.test and 3 more.',
    )
    expect(found).toContain('example.com: found.')
    expect(found).toContain(
      'example.org: Cloudflare has no zone example.org that this token can see',
    )

    // A change to the form puts the answer aside.
    await wrapper.get('#prov-token').setValue('other')
    expect(wrapper.find('[role="status"]').exists()).toBe(false)

    api.dnsProviders.test.mockRejectedValue(
      new Error('Cloudflare refused the token (9109 Unauthorized to access requested resource)'),
    )
    await button('Test').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Cloudflare refused the token')

    await wrapper.get('#prov-kind').setValue('exec')
    await flushPromises()
    expect(button('Test')).toBeUndefined()
  })
})
