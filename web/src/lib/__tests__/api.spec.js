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
