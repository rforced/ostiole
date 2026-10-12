import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import TokenDialog from '@/views/system/accounts/TokenDialog.vue'

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

function open(role = 'admin') {
  useAuthStore().user = { username: role, role }
  useConfigStore().replaceDraft({
    version: 12,
    zones: [],
    interfaces: [],
    rules: [],
    certificates: [{ id: 'web', source: 'self-signed' }],
  })
  return mount(TokenDialog, { props: { open: true }, global: { stubs } })
}

const metricsOnly = (w) =>
  w
    .findAll('input[type="checkbox"]')
    .find((i) => i.element.parentElement.textContent.includes('Metrics only'))

// A monitoring system's token reads the metrics and nothing else, so it
// is a viewer and fetches no certificates.
describe('TokenDialog metrics only', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('makes a viewer that fetches no certificates', async () => {
    const w = open()
    await w.get('#tok-name').setValue('scrape')
    await w.get('#tok-role').setValue('admin')
    expect(w.find('#tok-certs').exists()).toBe(true)
    await metricsOnly(w).setValue(true)
    expect(w.get('#tok-role').attributes('disabled')).toBeDefined()
    expect(w.find('#tok-certs').exists()).toBe(false)
    await w.get('form').trigger('submit')
    expect(w.emitted('create')[0][0]).toEqual({
      name: 'scrape',
      role: 'viewer',
      expiresInDays: 0,
      certificates: [],
      metrics: true,
    })
  })

  it('leaves an ordinary token without the flag', async () => {
    const w = open()
    await w.get('#tok-name').setValue('ci')
    await w.get('form').trigger('submit')
    expect(w.emitted('create')[0][0]).not.toHaveProperty('metrics')
  })
})

const roleOptions = (w) => w.findAll('#tok-role option').map((o) => o.element.value)

// A token cannot have more than its owner's role, and only an admin makes
// a certificate token.
describe('TokenDialog roles', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('offers an admin every role and the certificates', () => {
    const w = open('admin')
    expect(roleOptions(w)).toEqual(['admin', 'operator', 'viewer'])
    expect(w.find('#tok-certs').exists()).toBe(true)
  })

  it('offers an operator two roles and no certificates', () => {
    const w = open('operator')
    expect(roleOptions(w)).toEqual(['operator', 'viewer'])
    expect(w.find('#tok-certs').exists()).toBe(false)
  })

  it('offers a viewer one role', async () => {
    const w = open('viewer')
    expect(roleOptions(w)).toEqual(['viewer'])
    expect(w.find('#tok-certs').exists()).toBe(false)
    await w.get('#tok-name').setValue('dash')
    await w.get('form').trigger('submit')
    expect(w.emitted('create')[0][0]).toMatchObject({ role: 'viewer', certificates: [] })
  })
})
