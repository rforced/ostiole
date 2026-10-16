import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, UNAUTHORIZED_EVENT, api } from '@/lib/api'

function mockFetch(status, body, headers = { 'Content-Type': 'application/json' }) {
  const fn = vi
    .fn()
    .mockResolvedValue(
      new Response(body === undefined ? null : JSON.stringify(body), { status, headers }),
    )
  vi.stubGlobal('fetch', fn)
  return fn
}

describe('api', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('sends the CSRF header, credentials, and JSON bodies', async () => {
    const fetch = mockFetch(200, { username: 'admin', expires: 'x' })
    await api.auth.login('admin', 'pw')
    const [url, init] = fetch.mock.calls[0]
    expect(url).toBe('/api/v1/auth/login')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('same-origin')
    expect(init.headers['X-Requested-With']).toBe('ostiole')
    expect(init.headers['Content-Type']).toBe('application/json')
    expect(JSON.parse(init.body)).toEqual({ username: 'admin', password: 'pw' })
  })

  it('throws ApiError with server message and issues', async () => {
    mockFetch(422, { error: 'invalid configuration', issues: [{ path: 'zones', message: 'bad' }] })
    const err = await api.config.check({}).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(422)
    expect(err.message).toBe('invalid configuration')
    expect(err.issues).toHaveLength(1)
  })

  it('reads the revision of the saved configuration from its ETag', async () => {
    mockFetch(200, { version: 3 }, { 'Content-Type': 'application/json', ETag: '"ab12"' })
    expect(await api.config.get()).toEqual({ config: { version: 3 }, revision: 'ab12' })
    mockFetch(200, { version: 3 }, { 'Content-Type': 'application/json', ETag: 'W/"ab12"' })
    expect((await api.config.get()).revision).toBe('ab12')
  })

  it('applies a draft with the revision it was read from, and says when that is stale', async () => {
    const fetch = mockFetch(409, {
      error: 'the configuration changed since this draft was read',
      code: 'stale',
    })
    const err = await api.config.apply({ version: 3 }, 'ab12', 60).catch((e) => e)
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      config: { version: 3 },
      confirmTimeoutSeconds: 60,
      baseRevision: 'ab12',
    })
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(409)
    expect(err.code).toBe('stale')
  })

  it('dispatches the unauthorized event on 401, except for credential endpoints', async () => {
    mockFetch(401, { error: 'authentication required' })
    const handler = vi.fn()
    window.addEventListener(UNAUTHORIZED_EVENT, handler)
    await expect(api.status()).rejects.toMatchObject({ status: 401 })
    expect(handler).toHaveBeenCalledOnce()
    mockFetch(401, { error: 'invalid username or password' })
    await expect(api.auth.login('admin', 'wrong')).rejects.toMatchObject({ status: 401 })
    await expect(api.auth.changePassword('wrong', 'new one that is long')).rejects.toMatchObject({
      status: 401,
    })
    expect(handler).toHaveBeenCalledOnce()
    window.removeEventListener(UNAUTHORIZED_EVENT, handler)
  })

  // A wrong code, an ended sign-in and a wrong password to turn MFA off all
  // answer 401 while the account is signed in or half way
  // there: none of them is an expired session.
  it('does not read a refused MFA step as an expired session', async () => {
    mockFetch(401, { error: 'invalid code' })
    const handler = vi.fn()
    window.addEventListener(UNAUTHORIZED_EVENT, handler)
    await expect(api.auth.loginTOTP('tok', '000000')).rejects.toMatchObject({ status: 401 })
    await expect(api.auth.passkeyLoginOptions('tok')).rejects.toMatchObject({ status: 401 })
    await expect(api.auth.loginPasskey('tok', {})).rejects.toMatchObject({ status: 401 })
    await expect(api.auth.mfaClear('wrong')).rejects.toMatchObject({ status: 401 })
    expect(handler).not.toHaveBeenCalled()
    await expect(api.auth.mfa()).rejects.toMatchObject({ status: 401 })
    expect(handler).toHaveBeenCalledOnce()
    window.removeEventListener(UNAUTHORIZED_EVENT, handler)
  })

  it('sends the MFA calls where the router expects them', async () => {
    const fetch = mockFetch(200, {})
    const cases = [
      [() => api.auth.loginStart(), 'POST', '/auth/login/start'],
      [
        () => api.auth.loginTOTP('tok', '123456'),
        'POST',
        '/auth/login/totp',
        { pending: 'tok', code: '123456' },
      ],
      [
        () => api.auth.passkeyLoginOptions('tok'),
        'POST',
        '/auth/login/passkey/options',
        { pending: 'tok' },
      ],
      [
        () => api.auth.loginPasskey('tok', { id: 'c' }),
        'POST',
        '/auth/login/passkey',
        { pending: 'tok', credential: { id: 'c' } },
      ],
      [() => api.auth.mfa(), 'GET', '/auth/mfa'],
      [() => api.auth.totpStart(), 'POST', '/auth/mfa/totp/start'],
      [() => api.auth.totpConfirm('123456'), 'POST', '/auth/mfa/totp/confirm', { code: '123456' }],
      [() => api.auth.mfaClear('pw'), 'POST', '/auth/mfa/clear', { password: 'pw' }],
      [
        () => api.auth.passkeyOptions('pw'),
        'POST',
        '/auth/mfa/passkeys/options',
        { password: 'pw' },
      ],
      [
        () => api.auth.addPasskey('Laptop', { id: 'c' }),
        'POST',
        '/auth/mfa/passkeys',
        { name: 'Laptop', credential: { id: 'c' } },
      ],
      [() => api.auth.removePasskey('a/b'), 'DELETE', '/auth/mfa/passkeys/a%2Fb'],
      [() => api.users.clearMFA('sam'), 'DELETE', '/users/sam/mfa'],
    ]
    for (const [call, method, path, body] of cases) {
      fetch.mockClear()
      fetch.mockResolvedValue(
        new Response('{}', { headers: { 'Content-Type': 'application/json' } }),
      )
      await call()
      const [url, init] = fetch.mock.calls[0]
      expect([init.method, url], path).toEqual([method, `/api/v1${path}`])
      expect(init.body === undefined ? undefined : JSON.parse(init.body)).toEqual(body)
    }
  })

  it('returns undefined for 204 and text for text/plain', async () => {
    mockFetch(204, undefined, {})
    expect(await api.config.revert()).toBeUndefined()
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response('table inet ostiole', {
          status: 200,
          headers: { 'Content-Type': 'text/plain' },
        }),
      ),
    )
    expect(await api.ruleset()).toBe('table inet ostiole')
  })
})
