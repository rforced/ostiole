import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

// A signed-in admin, so the guard reaches the questions this is about.
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ bootstrap: vi.fn(), setupNeeded: false, loggedIn: true }),
}))

vi.mock('@/lib/api', () => ({
  api: { status: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

/** The one gate in front of a signed-in session: the setup wizard. */
describe('router guard', () => {
  let router

  beforeEach(async () => {
    setActivePinia(createPinia())
    sessionStorage.clear()
    vi.clearAllMocks()
    vi.resetModules()
    router = (await import('@/router')).default
  })

  it('sends an unconfigured router to the wizard', async () => {
    api.status.mockResolvedValue({ configured: false })
    await router.push('/')
    await router.isReady()
    expect(router.currentRoute.value.name).toBe('wizard')
  })

  it('honours "skip for now" until the tab closes', async () => {
    api.status.mockResolvedValue({ configured: false })
    sessionStorage.setItem('ostiole.skipWizard', '1')
    await router.push('/firewall/rules')
    expect(router.currentRoute.value.path).toBe('/firewall/rules')
  })

  it('leaves a configured router alone, and keeps it out of the wizard', async () => {
    api.status.mockResolvedValue({ configured: true })
    await router.push('/firewall/rules')
    expect(router.currentRoute.value.path).toBe('/firewall/rules')
    await router.push('/wizard')
    expect(router.currentRoute.value.name).toBe('dashboard')
  })
})
