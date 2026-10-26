import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { ApiError, api, type PendingLogin, type Role, type Session } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'

/**
 * The one sentence for what only an admin may do, said to an operator:
 * adminOnly('clear', 'it') is "Only an admin can clear it."
 */
export const adminOnly = (verb: string, object = '') =>
  `Only an admin can ${[verb, object].filter(Boolean).join(' ')}.`

/** Beside a setting only an admin may change, for everyone else. */
export const ADMIN_ONLY = adminOnly('change', 'this')

/** What each role is called where the account is shown. */
export const ROLE_LABELS: Record<Role, string> = {
  admin: 'Admin',
  operator: 'Operator',
  viewer: 'Viewer, read only',
}

/**
 * Tracks the current session and whether first-run setup is still needed.
 */
export const useAuthStore = defineStore('auth', () => {
  const user = ref<Session | null>(null)
  /** Null until known. */
  const setupNeeded = ref<boolean | null>(null)
  const ready = ref(false)
  /**
   * A sign-in whose password was right and which waits for MFA:
   * the token the router gave it, what it accepts, and the name a passkey
   * was made for. Null when no sign-in is waiting.
   */
  const pending = ref<PendingLogin | null>(null)

  const loggedIn = computed(() => user.value !== null)
  /**
   * Whether the account may change what only an admin may: command crons,
   * updates, the remote backup, management access and anti-lockout. The
   * server refuses those to anyone else at apply; the UI greys them out.
   */
  const isAdmin = computed(() => user.value?.role === 'admin')
  /**
   * Whether the account only looks. Nothing a viewer sends changes the
   * router, so settings show disabled and what acts is left out.
   */
  const readOnly = computed(() => user.value?.role === 'viewer')
  /**
   * Whether the account changes everything but what only an admin may:
   * the one that needs telling where that line is. A viewer changes
   * nothing, so the line means nothing to one.
   */
  const isOperator = computed(() => user.value?.role === 'operator')

  /** Resolve the session state once; safe to call repeatedly. */
  async function bootstrap() {
    if (ready.value) return
    try {
      user.value = await api.auth.me()
      setupNeeded.value = false
    } catch (e) {
      user.value = null
      if (e instanceof ApiError && e.status === 401) {
        try {
          setupNeeded.value = (await api.setup.status()).needed
        } catch {
          setupNeeded.value = null
        }
      } else {
        throw e
      }
    } finally {
      ready.value = true
    }
  }

  /** The session, or null when MFA is still due: see `pending`. */
  async function login(username: string, password: string) {
    const res = await api.auth.login(username, password)
    if ('pending' in res && res.pending?.token) {
      const { token, totp, passkeys, rpId } = res.pending
      pending.value = { token, totp: Boolean(totp), passkeys: Boolean(passkeys), rpId: rpId ?? '' }
      return null
    }
    return signedIn(res as Session)
  }

  async function loginTOTP(code: string) {
    return signedIn(await api.auth.loginTOTP(pending.value.token, code))
  }

  /** Asks the router for a challenge, the browser for the passkey, and the router to check it. */
  async function loginPasskey() {
    const token = pending.value.token
    const { publicKey } = await api.auth.passkeyLoginOptions(token)
    return loginWithPasskey(token, publicKey)
  }

  /**
   * Signs in with a passkey and no password: the router's request names no
   * account, and the passkey the browser gives names it. An aborted signal
   * stops it before the passkey is sent.
   */
  async function loginPasskeyAlone(signal?: AbortSignal) {
    const { token, publicKey } = await api.auth.loginStart()
    signal?.throwIfAborted()
    return loginWithPasskey(token, publicKey, signal)
  }

  /** Asks the browser for the passkey and the router to check it. */
  async function loginWithPasskey(
    token: string,
    publicKey: PublicKeyCredentialRequestOptionsJSON,
    signal?: AbortSignal,
  ) {
    const credential = await webauthn.get(publicKey, { signal })
    signal?.throwIfAborted()
    return signedIn(await api.auth.loginPasskey(token, credential))
  }

  /** Drops the waiting sign-in here. The router lets its token lapse by itself. */
  function cancelPending() {
    pending.value = null
  }

  /**
   * Leaves `pending` alone: the sign-in page keeps its second step on
   * screen while the next page loads, and the session's end clears it.
   */
  function signedIn(session: Session) {
    user.value = session
    setupNeeded.value = false
    return session
  }

  async function setup(username: string, password: string) {
    user.value = await api.setup.create(username, password)
    setupNeeded.value = false
  }

  async function logout() {
    try {
      await api.auth.logout()
    } finally {
      user.value = null
      pending.value = null
      useSystemStore().reset()
      useConfigStore().reset()
    }
  }

  /** Called when any request comes back 401. */
  function invalidate() {
    user.value = null
    pending.value = null
    useSystemStore().reset()
    useConfigStore().reset()
  }

  return {
    user,
    setupNeeded,
    ready,
    pending,
    loggedIn,
    isAdmin,
    isOperator,
    readOnly,
    bootstrap,
    login,
    loginTOTP,
    loginPasskey,
    loginPasskeyAlone,
    cancelPending,
    setup,
    logout,
    invalidate,
  }
})
