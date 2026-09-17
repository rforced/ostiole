import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

// A signed-in admin, so the guard reaches the questions this is about.
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ bootstrap: vi.fn(), setupNeeded: false, loggedIn: true }),
}))

vi.mock('@/lib/api', () => ({
  api: { host: { status: vi.fn() }, status: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

/**
 * The two gates in front of a signed-in session, and the one order they
 * have to agree on: a fresh router is usually unprepared *and*
 * unconfigured, and each gate must let the other's page through or the
 * browser bounces between them forever.
 */
describe('router guard', () => {
  let router

  beforeEach(async () => {
    setActivePinia(createPinia())
    sessionStorage.clear()
    vi.clearAllMocks()
    vi.resetModules()
    router = (await import('@/router')).default
  })

  it('sends a fresh router to the host page first, and stays there', async () => {
    api.host.status.mockResolvedValue({ prepared: false, steps: [] })
    api.status.mockResolvedValue({ configured: false })
    await router.push('/')
    await router.isReady()
    expect(router.currentRoute.value.path).toBe('/system/host')
    expect(router.currentRoute.value.query.from).toBe('/')
  })

  it('sends an unconfigured router to the wizard once the host is prepared', async () => {
    api.host.status.mockResolvedValue({ prepared: true, steps: [] })
    api.status.mockResolvedValue({ configured: false })
    await router.push('/')
    expect(router.currentRoute.value.name).toBe('wizard')
  })

  it('lets "continue for now" past the host gate and on to the wizard', async () => {
    api.host.status.mockResolvedValue({ prepared: false, steps: [] })
    api.status.mockResolvedValue({ configured: false })
    sessionStorage.setItem('ostiole.skipHost', '1')
    await router.push('/')
    expect(router.currentRoute.value.name).toBe('wizard')
  })

  it('leaves a prepared, configured router alone', async () => {
    api.host.status.mockResolvedValue({ prepared: true, steps: [] })
    api.status.mockResolvedValue({ configured: true })
    await router.push('/firewall/rules')
    expect(router.currentRoute.value.path).toBe('/firewall/rules')
  })
})
