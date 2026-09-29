import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import { api } from '@/lib/api'
import LoginView from '@/views/LoginView.vue'

vi.mock('@/lib/api', () => ({
  api: { auth: { loginPage: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

async function page() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:any(.*)*', component: LoginView }],
  })
  router.push('/login')
  await router.isReady()
  const wrapper = mount(LoginView, { global: { plugins: [router], stubs: { ThemeToggle: true } } })
  await flushPromises()
  return wrapper
}

/** The line under the heading. */
const subtitle = (wrapper) => wrapper.get('h1 + p')

describe('LoginView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('names the router by its hostname', async () => {
    api.auth.loginPage.mockResolvedValue({ hostname: 'edge' })
    const wrapper = await page()
    expect(subtitle(wrapper).text()).toBe('edge')
    expect(subtitle(wrapper).get('span').classes()).toContain('font-mono')
  })

  it('says Ostiole firewall when the router has no hostname', async () => {
    api.auth.loginPage.mockResolvedValue({})
    const wrapper = await page()
    expect(subtitle(wrapper).text()).toBe('Ostiole firewall')
    expect(subtitle(wrapper).get('span').classes()).not.toContain('invisible')
  })

  it('says Ostiole firewall when the router cannot be asked', async () => {
    api.auth.loginPage.mockRejectedValue(new Error('offline'))
    const wrapper = await page()
    expect(subtitle(wrapper).text()).toBe('Ostiole firewall')
    expect(subtitle(wrapper).get('span').classes()).not.toContain('invisible')
  })

  it('holds the line invisible while it asks', async () => {
    api.auth.loginPage.mockReturnValue(new Promise(() => {}))
    const wrapper = await page()
    expect(subtitle(wrapper).get('span').classes()).toContain('invisible')
  })
})
