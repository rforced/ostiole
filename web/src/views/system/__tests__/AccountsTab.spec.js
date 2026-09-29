import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useConfigStore } from '@/stores/config'
import AccountsTab from '@/views/system/certificates/AccountsTab.vue'
import DnsProvidersPage from '@/views/system/DnsProvidersPage.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: { certificates: { key: vi.fn(), list: vi.fn().mockResolvedValue({ providerKinds: [] }) } },
}))

function draft() {
  return {
    version: 11,
    zones: [],
    interfaces: [],
    rules: [],
    system: { management: {} },
    acme: {
      accounts: [
        { id: 'le', directory: 'https://acme-staging-v02.api.letsencrypt.org/directory' },
        { id: 'spare', directory: 'https://ca.example.test/dir' },
      ],
    },
    dnsProviders: [
      { id: 'cf', kind: 'cloudflare', settings: { token: 'x' }, domains: ['example.com'] },
      { id: 'unused', kind: 'hetzner', settings: { token: 'y' } },
    ],
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

const usedBy = (tr) => tr.get('td[data-label="Used by"]').text()
const deleteOf = (tr) => tr.findAll('button').find((b) => b.text() === 'Delete').element

// Delete stays in its place and is off; the Used by column says why.
describe('accounts and providers in use', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('refuses to delete an account a certificate orders from', async () => {
    const { wrapper } = await open(AccountsTab)
    const le = row(wrapper, 'le')
    expect(usedBy(le)).toBe('router')
    expect(deleteOf(le).disabled).toBe(true)
    const spare = row(wrapper, 'spare')
    expect(usedBy(spare)).toBe('—')
    expect(deleteOf(spare).disabled).toBe(false)
  })

  it('refuses to delete a provider a certificate uses', async () => {
    const { wrapper } = await open(DnsProvidersPage)
    const cf = row(wrapper, 'cf')
    expect(cf.text()).toContain('example.com')
    expect(usedBy(cf)).toBe('router')
    expect(deleteOf(cf).disabled).toBe(true)
    const unused = row(wrapper, 'unused')
    expect(usedBy(unused)).toBe('—')
    expect(deleteOf(unused).disabled).toBe(false)
  })
})
