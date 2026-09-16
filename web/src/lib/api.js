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

/** Turns a failed response into an ApiError, for the calls that fetch directly. */
async function apiError(res) {
  let message = res.statusText || `HTTP ${res.status}`
  let issues = []
  try {
    const err = await res.json()
    if (err?.error) message = err.error
    if (Array.isArray(err?.issues)) issues = err.issues
  } catch {
    /* non-JSON error body */
  }
  return new ApiError(res.status, message, issues)
}

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
  /** Everything the dashboard shows, in one request. */
  overview: () => get('/overview'),
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
    /** Compares two configurations; each side is a revision id, 'current', or inline. */
    diff: (body) => post('/config/diff', body),
    /** Downloads a backup; returns the file and the name the server chose. */
    backup: async ({ users = false, note = '' } = {}) => {
      const q = new URLSearchParams()
      if (users) q.set('users', 'true')
      if (note) q.set('note', note)
      const res = await fetch(`/api/v1/config/backup?${q}`, {
        headers: { 'X-Requested-With': 'ostiole' },
        credentials: 'same-origin',
      })
      if (!res.ok) throw await apiError(res)
      const name =
        /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ??
        'ostiole-backup.json'
      return { blob: await res.blob(), name }
    },
    /** Parses an uploaded backup and reports what it would change. */
    restore: async (file) => {
      const res = await fetch('/api/v1/config/restore', {
        method: 'POST',
        headers: { 'X-Requested-With': 'ostiole', 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: file,
      })
      if (!res.ok) throw await apiError(res)
      return res.json()
    },
  },
  ruleset: () => get('/ruleset'),
  counters: () => get('/counters'),
  interfaces: {
    live: () => get('/interfaces/live'),
  },
  /** Live gateway health from the multi-WAN monitor. */
  gateways: () => get('/gateways'),
  /** Default routes the kernel already has, with the gateway each would become. */
  detectedGateways: () => get('/gateways/detected'),
  policy: () => get('/policy'),
  diagnostics: {
    ping: (body) => post('/diagnostics/ping', body),
    traceroute: (body) => post('/diagnostics/traceroute', body),
    journal: (params = {}) => {
      const q = new URLSearchParams(
        Object.entries(params).filter(([, v]) => v !== '' && v !== undefined && v !== null),
      )
      return get(`/diagnostics/journal?${q}`)
    },
    states: (params = {}) => {
      const q = new URLSearchParams(
        Object.entries(params).filter(([, v]) => v !== '' && v !== undefined && v !== null),
      )
      return get(`/diagnostics/states?${q}`)
    },
    neighbours: () => get('/diagnostics/neighbours'),
    /** Downloads a pcap; returns the blob and the name the server chose. */
    capture: async (body) => {
      const res = await fetch('/api/v1/diagnostics/capture', {
        method: 'POST',
        headers: { 'X-Requested-With': 'ostiole', 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        let message = res.statusText
        try {
          message = (await res.json())?.error ?? message
        } catch {
          /* not JSON */
        }
        throw new ApiError(res.status, message)
      }
      const name =
        /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ??
        'capture.pcap'
      return { blob: await res.blob(), name }
    },
  },
  log: {
    recent: (limit = 200) => get(`/log/recent?limit=${limit}`),
  },
  aliases: {
    feeds: () => get('/aliases/feeds'),
    refresh: (name) => post(`/aliases/${encodeURIComponent(name)}/refresh`),
    refreshAll: () => post('/aliases/feeds/refresh'),
  },
  crons: {
    list: () => get('/crons'),
    run: (id) => post(`/crons/${encodeURIComponent(id)}/run`),
  },
  users: {
    list: () => get('/users'),
    setRole: (username, role) => post(`/users/${encodeURIComponent(username)}/role`, { role }),
  },
  tokens: {
    list: () => get('/tokens'),
    create: (body) => post('/tokens', body),
    remove: (id) => request('DELETE', `/tokens/${encodeURIComponent(id)}`),
  },
  certificate: {
    get: () => get('/certificate'),
    install: (certificate, key) => post('/certificate', { certificate, key }),
    regenerate: (hosts) => post('/certificate/self-signed', hosts ? { hosts } : {}),
  },
  services: {
    status: () => get('/services/status'),
    leases: () => get('/dhcp/leases'),
  },
  wireguard: {
    /** @returns {Promise<{privateKey: string, publicKey: string}>} */
    keys: () => post('/wireguard/keys', { kind: 'pair' }),
    /** @returns {Promise<{presharedKey: string}>} */
    psk: () => post('/wireguard/keys', { kind: 'psk' }),
  },
  update: {
    /** @returns {Promise<{check: object, status: object}>} */
    check: (channel = 'stable') => get(`/update/check?channel=${encodeURIComponent(channel)}`),
    status: () => get('/update/status'),
    apply: (channel = 'stable') => post('/update/apply', { channel }),
  },
}
