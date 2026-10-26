import { createPinia, setActivePinia } from 'pinia'
import type { Router } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api, type Status } from '@/lib/api'
import { fixture } from '@/test/fixtures'

// A signed-in admin, so the guard reaches the questions this is about.
const auth = vi.hoisted(() => ({ isAdmin: true }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ bootstrap: vi.fn(), setupNeeded: false, loggedIn: true, ...auth }),
}))

vi.mock('@/lib/api', () => ({
  api: { status: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

/** The one gate in front of a signed-in session: the setup wizard. */
describe('router guard', () => {
  let router: Router

  beforeEach(async () => {
    setActivePinia(createPinia())
    sessionStorage.clear()
    vi.clearAllMocks()
    vi.resetModules()
    auth.isAdmin = true
    router = (await import('@/router')).default
  })

  it('sends an unconfigured router to the wizard', async () => {
    vi.mocked(api.status).mockResolvedValue(fixture<Status>({ configured: false }))
    await router.push('/')
    await router.isReady()
    expect(router.currentRoute.value.name).toBe('wizard')
  })

  it('honours "skip for now" until the tab closes', async () => {
    vi.mocked(api.status).mockResolvedValue(fixture<Status>({ configured: false }))
    sessionStorage.setItem('ostiole.skipWizard', '1')
    await router.push('/firewall/rules')
    expect(router.currentRoute.value.path).toBe('/firewall/rules')
  })

  it('leaves a configured router alone, and keeps it out of the wizard', async () => {
    vi.mocked(api.status).mockResolvedValue(fixture<Status>({ configured: true }))
    await router.push('/firewall/rules')
    expect(router.currentRoute.value.path).toBe('/firewall/rules')
    await router.push('/wizard')
    expect(router.currentRoute.value.name).toBe('dashboard')
  })

  it('sends anyone but an admin from an admin page to their account', async () => {
    vi.mocked(api.status).mockResolvedValue(fixture<Status>({ configured: true }))
    await router.push('/system/accounts')
    expect(router.currentRoute.value.path).toBe('/system/accounts')
    auth.isAdmin = false
    await router.push('/system/general')
    await router.push('/system/accounts')
    expect(router.currentRoute.value.name).toBe('account')
    expect(router.currentRoute.value.meta.title).toBe('Account')
  })
})
