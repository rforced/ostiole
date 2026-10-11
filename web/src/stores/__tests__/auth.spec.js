import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
import { ADMIN_ONLY, adminOnly, useAuthStore } from '@/stores/auth'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: {
    auth: {
      login: vi.fn(),
      loginTOTP: vi.fn(),
      passkeyLoginOptions: vi.fn(),
      loginPasskey: vi.fn(),
    },
  },
}))
vi.mock('@/lib/webauthn', () => ({ get: vi.fn() }))

describe('adminOnly', () => {
  it('says what only an admin can do, one way', () => {
    expect(adminOnly('clear', 'it')).toBe('Only an admin can clear it.')
    expect(adminOnly('log', 'the router out')).toBe('Only an admin can log the router out.')
    expect(ADMIN_ONLY).toBe('Only an admin can change this.')
  })
})

describe('signing in with a second factor', () => {
  const session = { username: 'sam', role: 'admin' }
  const due = { secondFactor: { pending: 'tok', totp: true, passkeys: true, rpId: 'edge.example' } }

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('signs in at once when the account has none', async () => {
    api.auth.login.mockResolvedValue(session)
    const auth = useAuthStore()
    expect(await auth.login('sam', 'pw')).toEqual(session)
    expect(auth.loggedIn).toBe(true)
    expect(auth.pending).toBeNull()
  })

  it('signs in at once on a session that only says the account has one, as a bypass gives', async () => {
    api.auth.login.mockResolvedValue({ ...session, secondFactor: true })
    const auth = useAuthStore()
    expect(await auth.login('sam', 'pw')).toEqual({ ...session, secondFactor: true })
    expect(auth.pending).toBeNull()
  })

  it('waits for the second factor, and is not signed in until it comes', async () => {
    api.auth.login.mockResolvedValue(due)
    const auth = useAuthStore()
    expect(await auth.login('sam', 'pw')).toBeNull()
    expect(auth.loggedIn).toBe(false)
    expect(auth.pending).toEqual({ token: 'tok', totp: true, passkeys: true, rpId: 'edge.example' })

    api.auth.loginTOTP.mockResolvedValue(session)
    await auth.loginTOTP('123456')
    expect(api.auth.loginTOTP).toHaveBeenCalledWith('tok', '123456')
    expect(auth.user).toEqual(session)
  })

  it('signs in with a passkey: the options, the browser, then the router', async () => {
    api.auth.login.mockResolvedValue(due)
    api.auth.passkeyLoginOptions.mockResolvedValue({ publicKey: { challenge: 'c' } })
    webauthn.get.mockResolvedValue({ id: 'cred' })
    api.auth.loginPasskey.mockResolvedValue(session)
    const auth = useAuthStore()
    await auth.login('sam', 'pw')
    await auth.loginPasskey()
    expect(api.auth.passkeyLoginOptions).toHaveBeenCalledWith('tok')
    expect(webauthn.get).toHaveBeenCalledWith({ challenge: 'c' })
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('tok', { id: 'cred' })
    expect(auth.user).toEqual(session)
  })

  it('does not post a passkey the browser would not give', async () => {
    api.auth.login.mockResolvedValue(due)
    api.auth.passkeyLoginOptions.mockResolvedValue({ publicKey: {} })
    webauthn.get.mockRejectedValue(new Error('The passkey prompt was cancelled or timed out.'))
    const auth = useAuthStore()
    await auth.login('sam', 'pw')
    await expect(auth.loginPasskey()).rejects.toThrow('cancelled or timed out')
    expect(api.auth.loginPasskey).not.toHaveBeenCalled()
    expect(auth.loggedIn).toBe(false)
  })

  it('drops the waiting sign-in on cancel, with nothing sent to the router', async () => {
    api.auth.login.mockResolvedValue(due)
    const auth = useAuthStore()
    await auth.login('sam', 'pw')
    auth.cancelPending()
    expect(auth.pending).toBeNull()
    expect(api.auth.loginTOTP).not.toHaveBeenCalled()
  })
})
