import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import AppTopBar from '@/components/AppTopBar.vue'

vi.mock('@/lib/api', () => ({ api: {}, ApiError: class ApiError extends Error {} }))

const Page = { template: '<p>page</p>' }

async function shell() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:any(.*)*', component: Page }],
  })
  router.push('/')
  await router.isReady()
  const wrapper = mount(AppTopBar, { global: { plugins: [router] }, attachTo: document.body })
  return { router, wrapper }
}

const nav = () => document.querySelector('nav[aria-label="Main"]')
const link = (name) => [...nav().querySelectorAll('a')].find((a) => a.textContent.trim() === name)
const button = (name) =>
  [...nav().querySelectorAll('button')].find((b) => b.textContent.trim() === name)

async function open(wrapper) {
  await wrapper.get('button[aria-label="Open navigation"]').trigger('click')
  await flushPromises()
}

describe('AppTopBar', () => {
  beforeEach(() => setActivePinia(createPinia()))
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('opens the drawer with focus in it, and closes it once a page is picked', async () => {
    const { router, wrapper } = await shell()
    expect(nav()).toBeNull()

    await open(wrapper)
    expect(nav()).not.toBeNull()
    expect(document.querySelector('[role="dialog"]').contains(document.activeElement)).toBe(true)

    link('Routing').click()
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/routing')
    expect(nav()).toBeNull()
  })

  // Tapping the page you are on changes no route, and still closes it.
  it('closes when the current page is picked', async () => {
    const { wrapper } = await shell()
    await open(wrapper)
    link('Dashboard').click()
    await flushPromises()
    expect(nav()).toBeNull()
  })

  // A section opens its pages in place, so the drawer waits for the pick.
  it('stays open when a section is tapped', async () => {
    const { router, wrapper } = await shell()
    await open(wrapper)
    button('Firewall').click()
    await flushPromises()
    expect(nav()).not.toBeNull()
    expect(button('Firewall').getAttribute('aria-expanded')).toBe('true')

    link('Rules').click()
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/firewall/rules')
    expect(nav()).toBeNull()
  })

  it('closes when the route changes under it', async () => {
    const { router, wrapper } = await shell()
    await open(wrapper)
    await router.push('/interfaces')
    await flushPromises()
    expect(nav()).toBeNull()
  })
})
