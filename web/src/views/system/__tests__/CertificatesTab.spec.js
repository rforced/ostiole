import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import CertificatesTab from '@/views/system/certificates/CertificatesTab.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: {
    certificates: {
      list: vi.fn(),
      regenerate: vi.fn(),
      issue: vi.fn(),
      key: vi.fn(),
      fileURL: (id, name) => `/api/v1/certificates/${id}/files/${name}`,
      pkcs12: vi.fn(),
    },
  },
}))

const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template: '<div v-if="open"><slot /><slot name="footer" /></div>',
}

const builtIn = {
  names: ['fw.lan'],
  hosts: ['fw.lan', '10.0.0.1'],
  issuer: 'ostiole',
  notAfter: '2036-01-01T00:00:00Z',
  fingerprint: 'AA:BB',
  selfSigned: true,
}

function draft() {
  return {
    version: 11,
    zones: [{ name: 'wan', external: true }],
    interfaces: [{ name: 'eth0', zone: 'wan' }],
    rules: [],
    nat: {},
    system: { management: {} },
    certificates: [
      { id: 'router', enabled: true, source: 'acme', names: ['router.example.test'] },
      { id: 'mail', enabled: true, source: 'uploaded' },
      { id: 'draft-only', enabled: true, source: 'acme', names: ['new.example.test'] },
    ],
  }
}

async function open(page = {}, role = 'admin') {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  useAuthStore().user = { username: role, role }
  api.certificates.list.mockResolvedValue({
    builtIn,
    certificates: [],
    providerKinds: [],
    ...page,
  })
  const config = useConfigStore()
  config.replaceDraft(draft())
  const wrapper = mount(CertificatesTab, {
    global: { stubs: { AppDialog: AppDialogStub, RouterLink: true } },
    // The Download menu opens in the body, and moves focus about.
    attachTo: document.body,
  })
  await flushPromises()
  return { wrapper, config }
}

const row = (wrapper, id) =>
  wrapper.findAll('tr').find((tr) => tr.find('td').exists() && tr.get('td').text().startsWith(id))

const button = (tr, name) => tr.findAll('button').find((b) => b.text() === name)

describe('CertificatesTab', () => {
  beforeEach(() => setActivePinia(createPinia()))
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('shows what each certificate is doing', async () => {
    const { wrapper } = await open({
      certificates: [
        { id: 'router', source: 'acme', issued: true, notAfter: '2026-12-01T00:00:00Z' },
        { id: 'mail', source: 'uploaded', issued: true, expired: true },
      ],
    })
    expect(row(wrapper, 'router').get('.badge').text()).toBe('issued')
    expect(row(wrapper, 'mail').get('.badge').text()).toBe('expired')
    // A certificate that exists only in the draft says so rather than
    // claiming the CA refused it.
    expect(row(wrapper, 'draft-only').get('.badge').text()).toBe('apply to issue')
  })

  it('says why an order failed', async () => {
    const { wrapper } = await open({
      certificates: [{ id: 'router', source: 'acme', issued: false, lastError: 'the CA said no' }],
    })
    const badge = row(wrapper, 'router').get('.badge')
    expect(badge.text()).toBe('failed')
    expect(badge.attributes('title')).toBe('the CA said no')
  })

  it('offers downloads only for what is issued', async () => {
    const { wrapper } = await open({
      certificates: [{ id: 'router', source: 'acme', issued: true }],
    })
    expect(button(row(wrapper, 'router'), 'Download')).toBeDefined()
    expect(button(row(wrapper, 'draft-only'), 'Download')).toBeUndefined()
    // An uploaded certificate is not ordered, so there is nothing to press.
    expect(button(row(wrapper, 'mail'), 'Issue now')).toBeUndefined()
  })

  it('lists the files in the Download menu, then PKCS#12', async () => {
    const { wrapper } = await open({
      certificates: [{ id: 'router', source: 'acme', issued: true }],
    })
    await button(row(wrapper, 'router'), 'Download').trigger('click')
    await flushPromises()
    const items = [...document.querySelectorAll('[role="menuitem"]')]
    expect(items.map((i) => [i.textContent.trim(), i.getAttribute('href')])).toEqual([
      ['Full chain', '/api/v1/certificates/router/files/fullchain.pem'],
      ['Certificate', '/api/v1/certificates/router/files/cert.pem'],
      ['Chain', '/api/v1/certificates/router/files/chain.pem'],
      ['Private key', '/api/v1/certificates/router/files/key.pem'],
      ['PKCS#12', null],
    ])
    items.at(-1).click()
    await flushPromises()
    expect(wrapper.find('#p12-password').exists()).toBe(true)
  })

  // The menu holds the private key, which only an admin may take.
  it('has no Download menu for an operator', async () => {
    const { wrapper } = await open(
      { certificates: [{ id: 'router', source: 'acme', issued: true }] },
      'operator',
    )
    expect(button(row(wrapper, 'router'), 'Download')).toBeUndefined()
  })

  it('orders one now', async () => {
    const { wrapper } = await open({
      certificates: [{ id: 'router', source: 'acme', issued: false }],
    })
    await row(wrapper, 'router')
      .findAll('button')
      .find((b) => b.text() === 'Issue now')
      .trigger('click')
    expect(api.certificates.issue).toHaveBeenCalledWith('router')
  })

  it('says so when the server is not serving HTTPS', async () => {
    const { wrapper } = await open({ builtIn: null })
    expect(wrapper.text()).toContain('This server is not serving HTTPS.')
    expect(wrapper.find('#served-by').exists()).toBe(false)
  })

  it('picks what the web UI serves', async () => {
    const { wrapper, config } = await open()
    await wrapper.get('#served-by').setValue('router')
    expect(config.draft.system.management.certificate).toBe('router')
    await wrapper.get('#served-by').setValue('')
    expect(config.draft.system.management.certificate).toBeUndefined()
  })

  // What the web UI serves is management access, which only an admin may
  // change; the certificates themselves stay an operator's.
  it('keeps the served certificate from an operator', async () => {
    const { wrapper } = await open({}, 'operator')
    expect(wrapper.get('#served-by').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('Only an admin can change this.')
    const regenerate = wrapper.findAll('button').find((b) => b.text().includes('Regenerate'))
    expect(regenerate.attributes('disabled')).toBeDefined()
  })
})
