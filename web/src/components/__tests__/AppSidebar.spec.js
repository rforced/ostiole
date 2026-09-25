import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import AppSidebar from '@/components/AppSidebar.vue'

vi.mock('@/lib/api', () => ({ api: {}, ApiError: class ApiError extends Error {} }))

const Page = { template: '<p>page</p>' }

async function sidebar(path) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:any(.*)*', component: Page }],
  })
  router.push(path)
  await router.isReady()
  const wrapper = mount(AppSidebar, { global: { plugins: [router] }, attachTo: document.body })
  return { router, wrapper }
}

const section = (wrapper, name) => wrapper.findAll('nav button').find((b) => b.text() === name)
const expanded = (wrapper, name) => section(wrapper, name).attributes('aria-expanded') === 'true'
const pages = (wrapper, name) =>
  wrapper.get(`#${section(wrapper, name).attributes('aria-controls')}`)

describe('AppSidebar', () => {
  beforeEach(() => setActivePinia(createPinia()))
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('opens the section of the page you are on', async () => {
    const { wrapper } = await sidebar('/services/dns')
    expect(expanded(wrapper, 'Services')).toBe(true)
    expect(pages(wrapper, 'Services').isVisible()).toBe(true)
    expect(expanded(wrapper, 'Firewall')).toBe(false)
    expect(pages(wrapper, 'Firewall').isVisible()).toBe(false)
  })

  // A section is a list to pick from, not a page, and one open at a time
  // keeps the column short.
  it('opens one section at a time and goes nowhere', async () => {
    const { router, wrapper } = await sidebar('/')
    await section(wrapper, 'Firewall').trigger('click')
    expect(expanded(wrapper, 'Firewall')).toBe(true)
    await section(wrapper, 'Services').trigger('click')
    expect(expanded(wrapper, 'Firewall')).toBe(false)
    expect(expanded(wrapper, 'Services')).toBe(true)
    await section(wrapper, 'Services').trigger('click')
    expect(expanded(wrapper, 'Services')).toBe(false)
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('follows the route to the section it lands in', async () => {
    const { router, wrapper } = await sidebar('/firewall/rules')
    await section(wrapper, 'System').trigger('click')
    await router.push('/services/time')
    await flushPromises()
    expect(expanded(wrapper, 'Services')).toBe(true)
    expect(expanded(wrapper, 'System')).toBe(false)
  })
})
