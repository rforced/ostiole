/**
 * Passkeys through the browser's credentials API. The router sends every
 * byte string as base64url in JSON and reads the same back; the browser
 * wants ArrayBuffers. Newer browsers convert both ways themselves, older
 * ones are done by hand here.
 */

import type { AssertionResponse, AttestationResponse } from '@/lib/types.gen'

const NO_SUPPORT = 'This browser cannot use passkeys on this page.'

/** What a failed prompt means, said plainly. */
const REASONS: Record<string, string> = {
  NotAllowedError: 'The passkey prompt was cancelled or timed out.',
  AbortError: 'The passkey prompt was cancelled.',
  InvalidStateError: 'This device already has a passkey for this account.',
  NotSupportedError: 'This device cannot make a passkey of the kind the router asks for.',
  SecurityError: 'The browser would not use a passkey for this address.',
}

/** Whether this page can use passkeys: a secure context with the credentials API. */
export const available = () => typeof window !== 'undefined' && Boolean(window.PublicKeyCredential)

/** text is base64url, with or without padding. */
function bytes(text: string) {
  const b64 = text.replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(b64.padEnd(Math.ceil(b64.length / 4) * 4, '='))
  return Uint8Array.from(bin, (c) => c.charCodeAt(0)).buffer
}

function text(buf: ArrayBuffer | ArrayBufferView) {
  const view = ArrayBuffer.isView(buf)
    ? new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength)
    : new Uint8Array(buf)
  let bin = ''
  for (const b of view) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function creationOptions(
  json: PublicKeyCredentialCreationOptionsJSON,
): PublicKeyCredentialCreationOptions {
  return {
    ...json,
    challenge: bytes(json.challenge),
    user: { ...json.user, id: bytes(json.user.id) },
    excludeCredentials: (json.excludeCredentials ?? []).map((c) => ({ ...c, id: bytes(c.id) })),
  } as unknown as PublicKeyCredentialCreationOptions
}

function requestOptions(
  json: PublicKeyCredentialRequestOptionsJSON,
): PublicKeyCredentialRequestOptions {
  return {
    ...json,
    challenge: bytes(json.challenge),
    allowCredentials: (json.allowCredentials ?? []).map((c) => ({ ...c, id: bytes(c.id) })),
  } as unknown as PublicKeyCredentialRequestOptions
}

/**
 * A new credential as the router reads it. The router refuses a body with
 * a field it does not know, and toJSON() carries several, so only these go.
 */
function created(credential: PublicKeyCredential): AttestationResponse {
  const r = credential.response as AuthenticatorAttestationResponse
  const j: AttestationResponse =
    typeof credential.toJSON === 'function'
      ? (credential.toJSON() as AttestationResponse)
      : {
          id: credential.id,
          rawId: text(credential.rawId),
          type: credential.type,
          response: {
            clientDataJSON: text(r.clientDataJSON),
            attestationObject: text(r.attestationObject),
            transports: r.getTransports?.() ?? [],
          },
        }
  const out: AttestationResponse = {
    id: j.id,
    rawId: j.rawId,
    type: j.type,
    response: {
      clientDataJSON: j.response.clientDataJSON,
      attestationObject: j.response.attestationObject,
    },
  }
  if (j.response.transports?.length) out.response.transports = j.response.transports
  return out
}

/** A signed-in credential as the router reads it, for the same reason. */
function asserted(credential: PublicKeyCredential): AssertionResponse {
  const r = credential.response as AuthenticatorAssertionResponse
  const j: AssertionResponse =
    typeof credential.toJSON === 'function'
      ? (credential.toJSON() as AssertionResponse)
      : {
          id: credential.id,
          rawId: text(credential.rawId),
          type: credential.type,
          response: {
            clientDataJSON: text(r.clientDataJSON),
            authenticatorData: text(r.authenticatorData),
            signature: text(r.signature),
            userHandle: r.userHandle ? text(r.userHandle) : undefined,
          },
        }
  const out: AssertionResponse = {
    id: j.id,
    rawId: j.rawId,
    type: j.type,
    response: {
      clientDataJSON: j.response.clientDataJSON,
      authenticatorData: j.response.authenticatorData,
      signature: j.response.signature,
    },
  }
  if (j.response.userHandle) out.response.userHandle = j.response.userHandle
  return out
}

/**
 * An error from the browser as one the page can show. A cancelled or
 * aborted prompt keeps its name so the page can tell it apart.
 */
function reason(e: unknown) {
  const name = typeof e === 'object' && e !== null && 'name' in e ? String(e.name) : ''
  const out = new Error(REASONS[name] ?? (e instanceof Error ? e.message : String(e)), {
    cause: e,
  })
  if (name === 'NotAllowedError' || name === 'AbortError') out.name = name
  return out
}

/**
 * Asks the browser for a new passkey. The options are as the router sent
 * them; the result is the credential as the router reads it.
 */
export async function create(
  options: PublicKeyCredentialCreationOptionsJSON,
): Promise<AttestationResponse> {
  if (!available()) throw new Error(NO_SUPPORT)
  try {
    const publicKey =
      typeof PublicKeyCredential.parseCreationOptionsFromJSON === 'function'
        ? PublicKeyCredential.parseCreationOptionsFromJSON(options)
        : creationOptions(options)
    const credential = (await navigator.credentials.create({
      publicKey,
    })) as PublicKeyCredential | null
    if (!credential) throw new Error('No passkey was made.')
    return created(credential)
  } catch (e) {
    throw reason(e)
  }
}

/**
 * Asks the browser to sign in with a passkey. The options are as the router
 * sent them, a signal closes the prompt, and the result is the assertion as
 * the router reads it.
 */
export async function get(
  options: PublicKeyCredentialRequestOptionsJSON,
  { signal }: { signal?: AbortSignal } = {},
): Promise<AssertionResponse> {
  if (!available()) throw new Error(NO_SUPPORT)
  try {
    const publicKey =
      typeof PublicKeyCredential.parseRequestOptionsFromJSON === 'function'
        ? PublicKeyCredential.parseRequestOptionsFromJSON(options)
        : requestOptions(options)
    const credential = (await navigator.credentials.get(
      signal ? { publicKey, signal } : { publicKey },
    )) as PublicKeyCredential | null
    if (!credential) throw new Error('No passkey was used.')
    return asserted(credential)
  } catch (e) {
    throw reason(e)
  }
}
