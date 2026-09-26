import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import CertificateDialog from '@/views/system/certificates/CertificateDialog.vue'

// The dialog lives in a portal, which mount() cannot reach; open it in
// place so a test can type into it.
const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template: '<div v-if="open"><slot /></div>',
}

function draft() {
  return {
    version: 7,
    zones: [{ name: 'lan' }, { name: 'wan', external: true }],
    interfaces: [
      { name: 'eth1', zone: 'lan' },
      { name: 'eth0', zone: 'wan' },
    ],
    rules: [],
    nat: {},
    system: { management: {} },
    acme: {
      accounts: [{ id: 'le', directory: 'https://ca.test/dir' }],
      providers: [{ id: 'dns', kind: 'exec' }],
    },
  }
}

function open(certificate = null) {
  setActivePinia(createPinia())
  const config = useConfigStore()
  config.replaceDraft(draft())
  const wrapper = mount(CertificateDialog, {
    props: { open: true, certificate },
    global: { stubs: { AppDialog: AppDialogStub } },
  })
  return { wrapper, config }
}

const save = (wrapper) => wrapper.findAll('button').find((b) => b.text() === 'Save to draft')

describe('CertificateDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('locks an address to http-01 and the short-lived profile', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#cert-id').setValue('wan')
    await wrapper.get('#cert-names').setValue('198.51.100.4')
    await flushPromises()

    const challenge = wrapper.get('#cert-challenge')
    expect(challenge.element.value).toBe('http-01')
    expect(challenge.attributes('disabled')).toBeDefined()
    expect(wrapper.get('#cert-profile').element.value).toBe('shortlived')

    await wrapper.get('form').trigger('submit')
    expect(config.certificates[0]).toMatchObject({
      id: 'wan',
      challenge: 'http-01',
      profile: 'shortlived',
      names: ['198.51.100.4'],
    })
    // An http-01 certificate names no DNS provider.
    expect(config.certificates[0].provider).toBeUndefined()
  })

  it('locks a wildcard to dns-01', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#cert-id').setValue('star')
    await wrapper.get('#cert-names').setValue('*.example.test\nexample.test')
    await flushPromises()

    const challenge = wrapper.get('#cert-challenge')
    expect(challenge.element.value).toBe('dns-01')
    expect(challenge.attributes('disabled')).toBeDefined()

    await wrapper.get('form').trigger('submit')
    expect(config.certificates[0]).toMatchObject({
      challenge: 'dns-01',
      provider: 'dns',
      account: 'le',
      names: ['*.example.test', 'example.test'],
    })
  })

  it('offers the interfaces a CA could reach', () => {
    const { wrapper } = open()
    const boxes = wrapper.findAll('#cert-ifaces input[type=checkbox]')
    expect(boxes.map((b) => b.element.value)).toEqual(['eth0'])
  })

  it('needs both halves of an upload', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#cert-id').setValue('mail')
    await wrapper.get('#cert-source').setValue('uploaded')
    await flushPromises()
    expect(save(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.get('#cert-pem').setValue('-----BEGIN CERTIFICATE-----')
    expect(save(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.get('#cert-key').setValue('-----BEGIN PRIVATE KEY-----')
    expect(save(wrapper).attributes('disabled')).toBeUndefined()
    await wrapper.get('form').trigger('submit')
    expect(config.certificates[0]).toMatchObject({ id: 'mail', source: 'uploaded' })
    expect(config.certificates[0].account).toBeUndefined()
  })
})
