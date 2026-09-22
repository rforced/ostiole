import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useConfigStore } from '@/stores/config'
import AccountsTab from '@/views/system/certificates/AccountsTab.vue'
import ProvidersTab from '@/views/system/certificates/ProvidersTab.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: { certificates: { key: vi.fn(), list: vi.fn().mockResolvedValue({ providerKinds: [] }) } },
}))

function draft() {
  return {
    version: 6,
    zones: [],
    interfaces: [],
    rules: [],
    system: { management: {} },
    acme: {
      accounts: [
        { id: 'le', directory: 'https://acme-staging-v02.api.letsencrypt.org/directory' },
        { id: 'spare', directory: 'https://ca.example.test/dir' },
      ],
      providers: [
        { id: 'cf', kind: 'cloudflare', settings: { token: 'x' } },
        { id: 'unused', kind: 'desec', settings: { token: 'y' } },
      ],
    },
    certificates: [
      {
        id: 'router',
        enabled: true,
        source: 'acme',
        account: 'le',
        challenge: 'dns-01',
        provider: 'cf',
      },
    ],
  }
}

const row = (wrapper, id) =>
  wrapper.findAll('tr').find((tr) => tr.find('td').exists() && tr.get('td').text().startsWith(id))

async function open(component) {
  const config = useConfigStore()
  config.replaceDraft(draft())
  const wrapper = mount(component, { global: { stubs: { RouterLink: true } } })
  await flushPromises()
  return { wrapper, config }
}

describe('accounts and providers in use', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('refuses to delete an account a certificate orders from', async () => {
    const { wrapper } = await open(AccountsTab)
    const le = row(wrapper, 'le')
    // The label is short; the row's own column is what names the dependent.
    expect(le.text()).toContain('In use')
    expect(le.text()).toContain('router')
    expect(le.findAll('button').map((b) => b.text())).not.toContain('Delete')
    expect(
      row(wrapper, 'spare')
        .findAll('button')
        .map((b) => b.text()),
    ).toContain('Delete')
  })

  it('refuses to delete a provider a certificate uses', async () => {
    const { wrapper } = await open(ProvidersTab)
    const cf = row(wrapper, 'cf')
    expect(cf.text()).toContain('In use')
    expect(cf.text()).toContain('router')
    expect(cf.findAll('button').map((b) => b.text())).not.toContain('Delete')
    expect(
      row(wrapper, 'unused')
        .findAll('button')
        .map((b) => b.text()),
    ).toContain('Delete')
  })
})
