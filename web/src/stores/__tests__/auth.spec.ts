import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api, type Session } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
import { ADMIN_ONLY, adminOnly, useAuthStore } from '@/stores/auth'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: {
    auth: {
      loginStart: vi.fn(),
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

describe('signing in with MFA', () => {
  const session: Session = {
    username: 'sam',
    role: 'admin',
    created: '',
    lastSeen: '',
    expires: '',
    mfa: false,
  }
  const credential = {
    id: 'cred',
    rawId: 'cred',
    type: 'public-key',
    response: { clientDataJSON: '', authenticatorData: '', signature: '' },
  }
  const due = { pending: { token: 'tok', totp: true, passkeys: true, rpId: 'edge.example' } }

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('signs in at once when the account has none', async () => {
    vi.mocked(api.auth.login).mockResolvedValue(session)
    const auth = useAuthStore()
    expect(await auth.login('sam', 'pw')).toEqual(session)
    expect(auth.loggedIn).toBe(true)
    expect(auth.pending).toBeNull()
  })

  it('signs in at once on a session that only says the account has one, as a bypass gives', async () => {
    vi.mocked(api.auth.login).mockResolvedValue({ ...session, mfa: true })
    const auth = useAuthStore()
    expect(await auth.login('sam', 'pw')).toEqual({ ...session, mfa: true })
    expect(auth.pending).toBeNull()
  })

  it('waits for MFA, and is not signed in until it comes', async () => {
    vi.mocked(api.auth.login).mockResolvedValue(due)
    const auth = useAuthStore()
    expect(await auth.login('sam', 'pw')).toBeNull()
    expect(auth.loggedIn).toBe(false)
    expect(auth.pending).toEqual({ token: 'tok', totp: true, passkeys: true, rpId: 'edge.example' })

    vi.mocked(api.auth.loginTOTP).mockResolvedValue(session)
    await auth.loginTOTP('123456')
    expect(api.auth.loginTOTP).toHaveBeenCalledWith('tok', '123456')
    expect(auth.user).toEqual(session)
  })

  it('signs in with a passkey: the options, the browser, then the router', async () => {
    vi.mocked(api.auth.login).mockResolvedValue(due)
    vi.mocked(api.auth.passkeyLoginOptions).mockResolvedValue({ publicKey: { challenge: 'c' } })
    vi.mocked(webauthn.get).mockResolvedValue(credential)
    vi.mocked(api.auth.loginPasskey).mockResolvedValue(session)
    const auth = useAuthStore()
    await auth.login('sam', 'pw')
    await auth.loginPasskey()
    expect(api.auth.passkeyLoginOptions).toHaveBeenCalledWith('tok')
    expect(webauthn.get).toHaveBeenCalledWith({ challenge: 'c' }, { signal: undefined })
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('tok', credential)
    expect(auth.user).toEqual(session)
  })

  it('does not post a passkey the browser would not give', async () => {
    vi.mocked(api.auth.login).mockResolvedValue(due)
    vi.mocked(api.auth.passkeyLoginOptions).mockResolvedValue({ publicKey: { challenge: 'c' } })
    vi.mocked(webauthn.get).mockRejectedValue(
      new Error('The passkey prompt was cancelled or timed out.'),
    )
    const auth = useAuthStore()
    await auth.login('sam', 'pw')
    await expect(auth.loginPasskey()).rejects.toThrow('cancelled or timed out')
    expect(api.auth.loginPasskey).not.toHaveBeenCalled()
    expect(auth.loggedIn).toBe(false)
  })

  it('signs in with a passkey alone: the start, the browser, then the router', async () => {
    vi.mocked(api.auth.loginStart).mockResolvedValue({ token: 'pk', publicKey: { challenge: 'c' } })
    vi.mocked(webauthn.get).mockResolvedValue(credential)
    vi.mocked(api.auth.loginPasskey).mockResolvedValue(session)
    const auth = useAuthStore()
    const ctl = new AbortController()
    await auth.loginPasskeyAlone(ctl.signal)
    expect(api.auth.loginStart).toHaveBeenCalledWith()
    expect(webauthn.get).toHaveBeenCalledWith({ challenge: 'c' }, { signal: ctl.signal })
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('pk', credential)
    expect(auth.user).toEqual(session)
    expect(auth.pending).toBeNull()
  })

  it('opens no prompt once aborted while the router was asked', async () => {
    const ctl = new AbortController()
    vi.mocked(api.auth.loginStart).mockImplementation(async () => {
      ctl.abort()
      return { token: 'pk', publicKey: { challenge: 'c' } }
    })
    const auth = useAuthStore()
    await expect(auth.loginPasskeyAlone(ctl.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(webauthn.get).not.toHaveBeenCalled()
    expect(auth.loggedIn).toBe(false)
  })

  it('sends nothing once the prompt was aborted', async () => {
    const ctl = new AbortController()
    vi.mocked(api.auth.loginStart).mockResolvedValue({ token: 'pk', publicKey: { challenge: 'c' } })
    vi.mocked(webauthn.get).mockImplementation(async () => {
      ctl.abort()
      return credential
    })
    const auth = useAuthStore()
    await expect(auth.loginPasskeyAlone(ctl.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(api.auth.loginPasskey).not.toHaveBeenCalled()
    expect(auth.loggedIn).toBe(false)
  })

  it('drops the waiting sign-in on cancel, with nothing sent to the router', async () => {
    vi.mocked(api.auth.login).mockResolvedValue(due)
    const auth = useAuthStore()
    await auth.login('sam', 'pw')
    auth.cancelPending()
    expect(auth.pending).toBeNull()
    expect(api.auth.loginTOTP).not.toHaveBeenCalled()
  })
})
