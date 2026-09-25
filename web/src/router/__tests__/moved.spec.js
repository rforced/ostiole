import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ bootstrap: vi.fn(), setupNeeded: false, loggedIn: true }),
}))

vi.mock('@/lib/api', () => ({
  api: { status: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

/** A bookmark to a page that moved or was renamed still lands. */
describe('moved pages', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    api.status.mockResolvedValue({ configured: true })
  })

  it.each([
    ['/system/backup', '/system/configuration'],
    ['/services/dhcp6', '/services/dhcp#v6'],
    ['/services/leases', '/services/dhcp#leases'],
    ['/crons', '/system/crons'],
  ])('%s lands on %s', async (from, to) => {
    const router = (await import('@/router')).default
    await router.push(from)
    expect(router.currentRoute.value.fullPath).toBe(to)
  })
})
