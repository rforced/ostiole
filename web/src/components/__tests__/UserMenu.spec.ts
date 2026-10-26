import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import UserMenu from '@/components/UserMenu.vue'
import type { Session } from '@/lib/api'
import type { Config } from '@/lib/model.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

vi.mock('@/lib/api', () => ({ api: {}, ApiError: class ApiError extends Error {} }))

const Page = { template: '<p>page</p>' }

async function menu() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/login', name: 'login', component: Page },
      { path: '/:any(.*)*', component: Page },
    ],
  })
  router.push('/firewall/rules')
  await router.isReady()
  const wrapper = mount(UserMenu, { global: { plugins: [router] }, attachTo: document.body })
  await wrapper.get('button').trigger('click')
  await flushPromises()
  return { router, wrapper }
}

const item = (name: string) =>
  [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
    (e) => e.textContent.trim() === name,
  )

describe('UserMenu', () => {
  let auth
  beforeEach(() => {
    setActivePinia(createPinia())
    auth = useAuthStore()
    auth.user = { username: 'marlow', role: 'operator' } as Session
    vi.spyOn(auth, 'logout').mockResolvedValue(undefined)
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('shows the account and opens a menu of Account and Sign out', async () => {
    const { wrapper } = await menu()
    const trigger = wrapper.get('button')
    expect(trigger.attributes('aria-label')).toBe('Account menu')
    expect(trigger.attributes('aria-haspopup')).toBe('menu')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(trigger.text()).toContain('marlow')
    expect(trigger.text()).toContain('Operator')
    const names = [...document.querySelectorAll('[role="menuitem"]')].map((e) =>
      e.textContent.trim(),
    )
    expect(names).toEqual(['Account', 'Sign out'])
  })

  // The drawer on a phone closes on navigate, as it does for a nav link.
  it('goes to the account page and says so', async () => {
    const { router, wrapper } = await menu()
    expect(item('Account').getAttribute('href')).toBe('/account')
    item('Account').click()
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/account')
    expect(wrapper.emitted('navigate')).toHaveLength(1)
  })

  it('signs out from the trigger once the menu has closed', async () => {
    const { router, wrapper } = await menu()
    let seen = null
    auth.logout.mockImplementation(async () => {
      seen = document.activeElement
    })
    item('Sign out').click()
    await flushPromises()
    expect(auth.logout).toHaveBeenCalledOnce()
    expect(seen).toBe(wrapper.get('button').element)
    expect(router.currentRoute.value.name).toBe('login')
  })

  it('asks before throwing away a draft, and stays when told no', async () => {
    const config = useConfigStore()
    config.saved = { system: { hostname: 'alder' } } as Config
    config.draft = { system: { hostname: 'birch' } } as Config
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    const { router } = await menu()
    item('Sign out').click()
    await flushPromises()
    expect(ask).toHaveBeenCalledOnce()
    expect(auth.logout).not.toHaveBeenCalled()
    expect(router.currentRoute.value.path).toBe('/firewall/rules')

    ask.mockResolvedValue(true)
    await menu()
    item('Sign out').click()
    await flushPromises()
    expect(auth.logout).toHaveBeenCalledOnce()
  })

  // A viewer's draft differs only where a page filled in a default.
  it('does not ask a viewer', async () => {
    auth.user = { username: 'marlow', role: 'viewer' } as Session
    const config = useConfigStore()
    config.saved = { system: { hostname: 'alder' } } as Config
    config.draft = { system: { hostname: 'birch' } } as Config
    const ask = vi.spyOn(useConfirmStore(), 'ask')
    await menu()
    item('Sign out').click()
    await flushPromises()
    expect(ask).not.toHaveBeenCalled()
    expect(auth.logout).toHaveBeenCalledOnce()
  })
})
