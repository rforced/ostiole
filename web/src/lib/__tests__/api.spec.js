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

  it('dispatches the unauthorized event on 401', async () => {
    mockFetch(401, { error: 'authentication required' })
    const handler = vi.fn()
    window.addEventListener(UNAUTHORIZED_EVENT, handler)
    await expect(api.status()).rejects.toMatchObject({ status: 401 })
    expect(handler).toHaveBeenCalledOnce()
    window.removeEventListener(UNAUTHORIZED_EVENT, handler)
  })

  it('returns undefined for 204 and text for text/plain', async () => {
    mockFetch(204, undefined, {})
    expect(await api.config.revert()).toBeUndefined()
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response('table inet ostiole', {
            status: 200,
            headers: { 'Content-Type': 'text/plain' },
          }),
        ),
    )
    expect(await api.ruleset()).toBe('table inet ostiole')
  })
})
