import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api, type Status } from '@/lib/api'
import { fixture } from '@/test/fixtures'

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
    vi.mocked(api.status).mockResolvedValue(fixture<Status>({ configured: true }))
  })

  it.each([
    ['/system/backup', '/system/configuration'],
    ['/services/dhcp6', '/services/dhcp#v6'],
    ['/services/leases', '/services/dhcp#leases'],
    ['/crons', '/system/crons'],
    ['/system/ruleset', '/diagnostics/ruleset'],
    ['/system/certificates#providers', '/system/dns-providers'],
    ['/firewall/shaping#priorities', '/traffic/shaping#priorities'],
    ['/traffic#devices', '/traffic/usage#devices'],
  ])('%s lands on %s', async (from, to) => {
    const router = (await import('@/router')).default
    await router.push(from)
    expect(router.currentRoute.value.fullPath).toBe(to)
  })
})
