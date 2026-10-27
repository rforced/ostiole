import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { fixture, must } from '@/test/fixtures'
import { available, create, get } from '@/lib/webauthn'

const buf = (...n: number[]) => Uint8Array.from(n).buffer
interface Key {
  id: ArrayBuffer
}
const failure = (e: Error): Error => e
const rejected = (call: Promise<unknown>) =>
  call.then(() => {
    throw new Error('the call did not reject')
  }, failure)
const calls = {
  create:
    vi.fn<
      (o: {
        publicKey: { challenge: ArrayBuffer; user: Key; excludeCredentials: Key[] }
      }) => Promise<unknown>
    >(),
  get: vi.fn<
    (o: { publicKey: { allowCredentials: Key[] }; signal?: AbortSignal }) => Promise<unknown>
  >(),
}

describe('webauthn', () => {
  beforeEach(() => {
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    Object.defineProperty(navigator, 'credentials', { configurable: true, value: calls })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    Reflect.deleteProperty(navigator, 'credentials')
    vi.clearAllMocks()
  })

  it('is available only where the browser has passkeys', () => {
    expect(available()).toBe(true)
    vi.stubGlobal('PublicKeyCredential', undefined)
    expect(available()).toBe(false)
  })

  it('hands the browser the byte strings as bytes, and sends back only what the router reads', async () => {
    calls.create.mockResolvedValue({
      id: 'AQID',
      rawId: buf(1, 2, 3),
      type: 'public-key',
      authenticatorAttachment: 'platform',
      response: {
        clientDataJSON: buf(251, 255),
        attestationObject: buf(250),
        getTransports: () => ['internal'],
      },
    })
    const sent = await create(
      fixture<PublicKeyCredentialCreationOptionsJSON>({
        challenge: 'AQID',
        user: { id: 'BAU', name: 'sam', displayName: 'sam' },
        excludeCredentials: [{ type: 'public-key', id: '-_8' }],
      }),
    )

    const { publicKey } = must(calls.create.mock.calls[0])[0]
    expect([...new Uint8Array(publicKey.challenge)]).toEqual([1, 2, 3])
    expect([...new Uint8Array(publicKey.user.id)]).toEqual([4, 5])
    expect([...new Uint8Array(must(publicKey.excludeCredentials[0]).id)]).toEqual([251, 255])
    expect(sent).toEqual({
      id: 'AQID',
      rawId: 'AQID',
      type: 'public-key',
      response: { clientDataJSON: '-_8', attestationObject: '-g', transports: ['internal'] },
    })
  })

  it('drops what toJSON adds, which the router would refuse', async () => {
    calls.get.mockResolvedValue({
      toJSON: () => ({
        id: 'AQID',
        rawId: 'AQID',
        type: 'public-key',
        authenticatorAttachment: 'platform',
        clientExtensionResults: {},
        response: {
          clientDataJSON: 'Y2Q',
          authenticatorData: 'YXV0aA',
          signature: 'c2ln',
          userHandle: 'dXNlcg',
        },
      }),
    })
    const sent = await get({
      challenge: 'AQID',
      allowCredentials: [{ type: 'public-key', id: 'AQID' }],
    })
    expect(sent).toEqual({
      id: 'AQID',
      rawId: 'AQID',
      type: 'public-key',
      response: {
        clientDataJSON: 'Y2Q',
        authenticatorData: 'YXV0aA',
        signature: 'c2ln',
        userHandle: 'dXNlcg',
      },
    })
  })

  it('asks for the credentials the router listed', async () => {
    calls.get.mockResolvedValue({
      id: 'AQID',
      rawId: buf(1, 2, 3),
      type: 'public-key',
      response: {
        clientDataJSON: buf(1),
        authenticatorData: buf(2),
        signature: buf(3),
        userHandle: null,
      },
    })
    const sent = await get({
      challenge: 'AQID',
      allowCredentials: [{ type: 'public-key', id: 'BAU' }],
    })
    const { publicKey } = must(calls.get.mock.calls[0])[0]
    expect([...new Uint8Array(must(publicKey.allowCredentials[0]).id)]).toEqual([4, 5])
    expect(sent.response).toEqual({
      clientDataJSON: 'AQ',
      authenticatorData: 'Ag',
      signature: 'Aw',
    })
  })

  it('says a cancelled prompt in a sentence', async () => {
    calls.create.mockRejectedValue(new DOMException('denied', 'NotAllowedError'))
    await expect(
      create(
        fixture<PublicKeyCredentialCreationOptionsJSON>({ challenge: 'AQID', user: { id: 'BAU' } }),
      ),
    ).rejects.toThrow('The passkey prompt was cancelled or timed out.')
    calls.create.mockRejectedValue(new DOMException('exists', 'InvalidStateError'))
    await expect(
      create(
        fixture<PublicKeyCredentialCreationOptionsJSON>({ challenge: 'AQID', user: { id: 'BAU' } }),
      ),
    ).rejects.toThrow('This device already has a passkey for this account.')
  })

  it('hands the browser the signal, and an aborted prompt keeps its name', async () => {
    const ctl = new AbortController()
    calls.get.mockRejectedValue(new DOMException('aborted', 'AbortError'))
    const err = await rejected(get({ challenge: 'AQID' }, { signal: ctl.signal }))
    expect(must(calls.get.mock.calls[0])[0].signal).toBe(ctl.signal)
    expect(err.name).toBe('AbortError')
    expect(err.message).toBe('The passkey prompt was cancelled.')

    calls.get.mockRejectedValue(new DOMException('denied', 'NotAllowedError'))
    expect((await rejected(get({ challenge: 'AQID' }))).name).toBe('NotAllowedError')
    calls.get.mockRejectedValue(new DOMException('no', 'SecurityError'))
    expect((await rejected(get({ challenge: 'AQID' }))).name).toBe('Error')
  })

  it('does not reach for a browser that has no passkeys', async () => {
    vi.stubGlobal('PublicKeyCredential', undefined)
    await expect(get({ challenge: 'AQID' })).rejects.toThrow(
      'This browser cannot use passkeys on this page.',
    )
    expect(calls.get).not.toHaveBeenCalled()
  })
})
