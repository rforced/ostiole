/**
 * Thin fetch wrapper for the Ostiole API.
 *
 * Every state-changing request carries the X-Requested-With header the
 * server demands (its CSRF defence). A 401 dispatches "ostiole:unauthorized"
 * on window so the router can send the user to the login page.
 */

export const UNAUTHORIZED_EVENT = 'ostiole:unauthorized'

export class ApiError extends Error {
  /**
   * @param {number} status
   * @param {string} message
   * @param {Array<{path: string, message: string}>} [issues]
   */
  constructor(status, message, issues = []) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.issues = issues
  }
}

/** Endpoints where a 401 means "wrong credentials", not "session expired". */
const CREDENTIAL_PATHS = ['/auth/login', '/auth/password', '/setup']

/**
 * @template T
 * @param {string} method
 * @param {string} path
 * @param {unknown} [body]
 * @returns {Promise<T>}
 */
async function request(method, path, body) {
  /** @type {Record<string, string>} */
  const headers = { Accept: 'application/json', 'X-Requested-With': 'ostiole' }
  /** @type {RequestInit} */
  const init = { method, headers, credentials: 'same-origin' }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const res = await fetch(`/api/v1${path}`, init)
  if (res.status === 401 && !CREDENTIAL_PATHS.includes(path)) {
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path } }))
  }
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`
    let issues = []
    try {
      const err = await res.json()
      if (err?.error) message = err.error
      if (Array.isArray(err?.issues)) issues = err.issues
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, message, issues)
  }
  if (res.status === 204) return /** @type {T} */ (undefined)
  const type = res.headers.get('Content-Type') ?? ''
  if (type.startsWith('text/')) return /** @type {T} */ (await res.text())
  return res.json()
}

const get = (path) => request('GET', path)
const post = (path, body) => request('POST', path, body)

/** @typedef {{ status: string, version: string, commit: string }} Health */
/** @typedef {{ username: string, expires: string }} Session */
/** @typedef {{ configured: boolean, tableLoaded: boolean, network: string, pending?: { since: string, deadline: string, remaining: number } }} Status */

export const api = {
  /** @returns {Promise<Health>} */
  health: () => get('/health'),
  setup: {
    /** @returns {Promise<{needed: boolean}>} */
    status: () => get('/setup'),
    /** @returns {Promise<Session>} */
    create: (username, password) => post('/setup', { username, password }),
  },
  auth: {
    /** @returns {Promise<Session>} */
    login: (username, password) => post('/auth/login', { username, password }),
    logout: () => post('/auth/logout'),
    /** @returns {Promise<Session>} */
    me: () => get('/auth/me'),
    /** @returns {Promise<Session>} */
    changePassword: (current, next) => post('/auth/password', { current, new: next }),
  },
  /** @returns {Promise<Status>} */
  status: () => get('/status'),
  config: {
    get: () => get('/config'),
    /** Build (without saving) a first configuration from the wizard answers. */
    starter: (opts) => post('/config/starter', opts),
    revisions: () => get('/config/revisions'),
    revision: (id) => get(`/config/revisions/${encodeURIComponent(id)}`),
    check: (config) => post('/check', { config }),
    apply: (config, confirmTimeoutSeconds = 60) =>
      post('/apply', { config, confirmTimeoutSeconds }),
    confirm: () => post('/apply/confirm'),
    revert: () => post('/apply/revert'),
  },
  ruleset: () => get('/ruleset'),
  counters: () => get('/counters'),
  interfaces: {
    live: () => get('/interfaces/live'),
  },
}
