import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import { NAV } from '@/lib/nav'
import { useAuthStore } from '@/stores/auth'
import AccountsPage from '@/views/system/AccountsPage.vue'
import { sessionOf, must } from '@/test/fixtures'

const { tabs } = must(
  must(must(NAV.find((i) => i.to === '/system')).pages).find((p) => p.path === 'accounts'),
)

/** Mounts the page at a path for a role, with the sections stubbed. */
async function page(path: string, role: string) {
  useAuthStore().user = sessionOf({ username: role, role })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/system/accounts', component: AccountsPage, meta: { tabs } }],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(AccountsPage, {
    global: {
      plugins: [router],
      stubs: {
        AccountsSection: true,
        TokensSection: true,
        AuditLogSection: true,
      },
    },
  })
  await flushPromises()
  return wrapper
}

const tabNames = (wrapper: VueWrapper) => wrapper.findAll('[role="tab"]').map((t) => t.text())
const cards = (wrapper: VueWrapper) =>
  ['accounts-section-stub', 'tokens-section-stub'].map((s) => wrapper.find(s).exists())

describe('AccountsPage', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('gives an admin the accounts and the audit log', async () => {
    const wrapper = await page('/system/accounts', 'admin')
    expect(tabNames(wrapper)).toEqual(['Accounts', 'Audit log'])
    expect(cards(wrapper)).toEqual([true, true])
    expect(wrapper.find('audit-log-section-stub').exists()).toBe(false)
  })

  it('opens the audit log a link names', async () => {
    const wrapper = await page('/system/accounts#audit', 'admin')
    expect(wrapper.find('audit-log-section-stub').exists()).toBe(true)
  })

  it('keeps the page as it was, with no tabs, for anyone else', async () => {
    for (const role of ['operator', 'viewer']) {
      const wrapper = await page('/system/accounts#audit', role)
      expect(tabNames(wrapper)).toEqual([])
      expect(cards(wrapper)).toEqual([true, true])
      expect(wrapper.find('audit-log-section-stub').exists()).toBe(false)
    }
  })
})
