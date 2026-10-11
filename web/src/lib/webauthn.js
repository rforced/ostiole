/**
 * Passkeys through the browser's credentials API. The router sends every
 * byte string as base64url in JSON and reads the same back; the browser
 * wants ArrayBuffers. Newer browsers convert both ways themselves, older
 * ones are done by hand here.
 */

const NO_SUPPORT = 'This browser cannot use passkeys on this page.'

/** What a failed prompt means, said plainly. */
const REASONS = {
  NotAllowedError: 'The passkey prompt was cancelled or timed out.',
  AbortError: 'The passkey prompt was cancelled.',
  InvalidStateError: 'This device already has a passkey for this account.',
  NotSupportedError: 'This device cannot make a passkey of the kind the router asks for.',
  SecurityError: 'The browser would not use a passkey for this address.',
}

/** Whether this page can use passkeys: a secure context with the credentials API. */
export const available = () => typeof window !== 'undefined' && Boolean(window.PublicKeyCredential)

/** @param {string} text base64url, with or without padding */
function bytes(text) {
  const b64 = text.replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(b64.padEnd(Math.ceil(b64.length / 4) * 4, '='))
  return Uint8Array.from(bin, (c) => c.charCodeAt(0)).buffer
}

/** @param {ArrayBuffer | ArrayBufferView} buf */
function text(buf) {
  const view = ArrayBuffer.isView(buf)
    ? new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength)
    : new Uint8Array(buf)
  let bin = ''
  for (const b of view) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function creationOptions(json) {
  return {
    ...json,
    challenge: bytes(json.challenge),
    user: { ...json.user, id: bytes(json.user.id) },
    excludeCredentials: (json.excludeCredentials ?? []).map((c) => ({ ...c, id: bytes(c.id) })),
  }
}

function requestOptions(json) {
  return {
    ...json,
    challenge: bytes(json.challenge),
    allowCredentials: (json.allowCredentials ?? []).map((c) => ({ ...c, id: bytes(c.id) })),
  }
}

/**
 * A new credential as the router reads it. The router refuses a body with
 * a field it does not know, and toJSON() carries several, so only these go.
 */
function created(credential) {
  const r = credential.response
  const j =
    typeof credential.toJSON === 'function'
      ? credential.toJSON()
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
  const out = {
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
function asserted(credential) {
  const r = credential.response
  const j =
    typeof credential.toJSON === 'function'
      ? credential.toJSON()
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
  const out = {
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

/** An error from the browser as one the page can show. */
function reason(e) {
  return new Error(REASONS[e?.name] ?? (e instanceof Error ? e.message : String(e)), { cause: e })
}

/**
 * Asks the browser for a new passkey.
 * @param {object} options PublicKeyCredentialCreationOptions as the router sent them
 * @returns {Promise<object>} the credential, as the router reads it
 */
export async function create(options) {
  if (!available()) throw new Error(NO_SUPPORT)
  try {
    const publicKey =
      typeof PublicKeyCredential.parseCreationOptionsFromJSON === 'function'
        ? PublicKeyCredential.parseCreationOptionsFromJSON(options)
        : creationOptions(options)
    const credential = await navigator.credentials.create({ publicKey })
    if (!credential) throw new Error('No passkey was made.')
    return created(credential)
  } catch (e) {
    throw reason(e)
  }
}

/**
 * Asks the browser to sign in with a passkey.
 * @param {object} options PublicKeyCredentialRequestOptions as the router sent them
 * @returns {Promise<object>} the assertion, as the router reads it
 */
export async function get(options) {
  if (!available()) throw new Error(NO_SUPPORT)
  try {
    const publicKey =
      typeof PublicKeyCredential.parseRequestOptionsFromJSON === 'function'
        ? PublicKeyCredential.parseRequestOptionsFromJSON(options)
        : requestOptions(options)
    const credential = await navigator.credentials.get({ publicKey })
    if (!credential) throw new Error('No passkey was used.')
    return asserted(credential)
  } catch (e) {
    throw reason(e)
  }
}
