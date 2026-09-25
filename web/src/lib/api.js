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

/**
 * Appends the filter parameters that are actually set. An empty one means
 * "any", and sending it would narrow the answer to rows with an empty
 * field.
 * @param {string} path
 * @param {Record<string, unknown>} params
 */
function withQuery(path, params = {}) {
  const q = new URLSearchParams(
    Object.entries(params)
      .filter(([, v]) => v !== '' && v !== undefined && v !== null)
      .map(([k, v]) => [k, String(v)]),
  )
  const s = q.toString()
  return s ? `${path}?${s}` : path
}

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

/**
 * Base64 for a JSON body, chunked so a large file does not blow the
 * argument limit of String.fromCharCode.
 * @param {ArrayBuffer} buf
 */
function base64(buf) {
  const bytes = new Uint8Array(buf)
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  }
  return btoa(s)
}

/** @typedef {{ status: string, version: string, commit?: string }} Health */
/** @typedef {{ username: string, role: 'admin' | 'operator' | 'viewer', created: string, lastSeen: string, expires: string }} Session */
/** @typedef {{ configured: boolean, tableLoaded: boolean, network: string, pending?: { since: string, deadline: string, remaining: number }, fallback?: { since: string, reason: string }, recovered?: { since: string, at: string }, ssh?: string }} Status */

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
  /**
   * CPU, memory and disk. The CPU figure covers the time since the previous
   * request, so the first one after a restart has none.
   */
  systemStats: () => get('/system/stats'),
  /** The zones this router can be set to, and the one its clock reads now. */
  timezones: () => get('/system/timezones'),
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
    /**
     * Downloads a backup; returns the file and the name the server chose.
     * It is a POST because a passphrase has no business in a URL.
     */
    backup: async ({ users = false, note = '', passphrase = '', redact = false } = {}) => {
      const res = await fetch('/api/v1/config/backup', {
        method: 'POST',
        headers: { 'X-Requested-With': 'ostiole', 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ users, note, passphrase, redact }),
      })
      if (!res.ok) throw await apiError(res)
      const name =
        /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ??
        'ostiole-backup.json'
      return { blob: await res.blob(), name }
    },
    /** The backups this router has in its bucket, newest first. */
    remoteCopies: () => get('/config/backup/remote'),
    /** Reads one backup out of the bucket and reports what it would change. */
    restoreRemote: (key, passphrase = '') =>
      post('/config/backup/remote/restore', { key, passphrase }),
    /** Parses an uploaded backup and reports what it would change. */
    restore: async (file, passphrase = '') => {
      const data = base64(await file.arrayBuffer())
      const res = await fetch('/api/v1/config/restore', {
        method: 'POST',
        headers: { 'X-Requested-With': 'ostiole', 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ data, passphrase }),
      })
      if (!res.ok) throw await apiError(res)
      return res.json()
    },
  },
  ruleset: () => get('/ruleset'),
  counters: () => get('/counters'),
  /** The rules Ostiole adds on its own for a configuration, in evaluation order. */
  systemRules: (config) => post('/rules/system', { config }),
  /** The outbound NAT rules a configuration makes Ostiole write on its own: the automatic masquerade. */
  systemNat: (config) => post('/nat/system', { config }),
  /** The names a configuration makes the DNS server answer on its own: static leases with hostnames. */
  systemHosts: (config) => post('/dns/system-hosts', { config }),
  /** Empty the caches the router is running now. Answers with what it cleared. */
  clearDnsCache: () => request('DELETE', '/dns/cache'),
  interfaces: {
    live: () => get('/interfaces/live'),
    /** Ask for a fresh lease; release drops the current one first. */
    renew: (name, release = false) =>
      post(`/interfaces/${encodeURIComponent(name)}/renew`, { release }),
  },
  /** Live gateway health from the multi-WAN monitor. */
  gateways: () => get('/gateways'),
  /** Default routes the kernel already has, with the gateway each would become. */
  detectedGateways: () => get('/gateways/detected'),
  policy: () => get('/policy'),
  /** Line speeds per interface and how the queues behind them are doing. */
  shaping: () => get('/shaping'),
  diagnostics: {
    ping: (body) => post('/diagnostics/ping', body),
    traceroute: (body) => post('/diagnostics/traceroute', body),
    journal: (params = {}) => get(withQuery('/diagnostics/journal', params)),
    states: (params = {}) => get(withQuery('/diagnostics/states', params)),
    neighbours: () => get('/diagnostics/neighbours'),
    /** What the cable modem reports; refresh reads it again instead of the minute-old copy. */
    modem: (address, refresh = false) => {
      const q = new URLSearchParams({ address })
      if (refresh) q.set('refresh', '1')
      return get(`/diagnostics/modem?${q}`)
    },
    /** Every drive in this router, with the tool's version and whether we are root. */
    drives: () => get('/diagnostics/drives'),
    drive: (name) => get(`/diagnostics/drives/${encodeURIComponent(name)}`),
    /** Starts a self-test and answers with the drive as it reads afterwards. */
    selfTest: (name, kind) =>
      post(`/diagnostics/drives/${encodeURIComponent(name)}/self-test`, { kind }),
    abortSelfTest: (name) =>
      request('DELETE', `/diagnostics/drives/${encodeURIComponent(name)}/self-test`),
    /** Downloads one drive's full report; returns the blob and the name the server chose. */
    driveReport: async (name) => {
      const res = await fetch(`/api/v1/diagnostics/drives/${encodeURIComponent(name)}/report`, {
        headers: { 'X-Requested-With': 'ostiole' },
        credentials: 'same-origin',
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
      const filename =
        /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ??
        `ostiole-${name}-smart.txt`
      return { blob: await res.blob(), name: filename }
    },
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
    /** Reads a list no alias names yet: its size and what it can be narrowed by. */
    inspect: (url) => post('/aliases/inspect', { url }),
  },
  blocking: {
    status: () => get('/blocking'),
    catalog: () => get('/blocking/catalog'),
    lookup: (name) => get(`/blocking/lookup?name=${encodeURIComponent(name)}`),
    refresh: (name) => post(`/blocking/lists/${encodeURIComponent(name)}/refresh`),
    refreshAll: () => post('/blocking/refresh'),
    import: (name, body) => post(`/blocking/lists/${encodeURIComponent(name)}/import`, body),
  },
  /** What the DNS server answered, while the query log is on. */
  queries: {
    list: (params = {}) => get(withQuery('/dns/queries', params)),
    summary: () => get('/dns/queries/summary'),
    clear: () => request('DELETE', '/dns/queries'),
  },
  crons: {
    list: () => get('/crons'),
    run: (id) => post(`/crons/${encodeURIComponent(id)}/run`),
  },
  notifications: {
    /** How mail and the webhook have done, and what went out lately. */
    status: () => get('/notifications'),
    /** Sends a test to the targets these settings switch on. */
    test: (settings) => post('/notifications/test', settings),
  },
  users: {
    list: () => get('/users'),
    create: (body) => post('/users', body),
    remove: (username) => request('DELETE', `/users/${encodeURIComponent(username)}`),
    setRole: (username, role) => post(`/users/${encodeURIComponent(username)}/role`, { role }),
    setPassword: (username, password) =>
      post(`/users/${encodeURIComponent(username)}/password`, { password }),
    rename: (username, next) =>
      post(`/users/${encodeURIComponent(username)}/username`, { username: next }),
  },
  tokens: {
    list: () => get('/tokens'),
    create: (body) => post('/tokens', body),
    remove: (id) => request('DELETE', `/tokens/${encodeURIComponent(id)}`),
  },
  certificates: {
    list: () => get('/certificates'),
    regenerate: (hosts) => post('/certificates/self-signed', hosts ? { hosts } : {}),
    /** A fresh account key, so one never has to be pasted in. */
    key: () => post('/certificates/keys', {}),
    issue: (id) => post(`/certificates/${encodeURIComponent(id)}/issue`, {}),
    fileURL: (id, name) =>
      `/api/v1/certificates/${encodeURIComponent(id)}/files/${encodeURIComponent(name)}`,
    /** Downloads a PKCS#12 bundle; returns the blob and the name the server chose. */
    pkcs12: async (id, password) => {
      const res = await fetch(`/api/v1/certificates/${encodeURIComponent(id)}/pkcs12`, {
        method: 'POST',
        headers: { 'X-Requested-With': 'ostiole', 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ password }),
      })
      if (res.status === 401) {
        window.dispatchEvent(
          new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path: `/certificates/${id}/pkcs12` } }),
        )
      }
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
        /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? `${id}.p12`
      return { blob: await res.blob(), name }
    },
  },
  services: {
    status: () => get('/services/status'),
    leases: () => get('/dhcp/leases'),
  },
  upnp: {
    mappings: () => get('/upnp/mappings'),
  },
  ntp: {
    /** @returns {Promise<object>} whether the clock follows a server, the servers, and the defaults */
    status: () => get('/ntp/status'),
  },
  wol: {
    /** @param {{interface: string, mac: string}} body */
    wake: (body) => post('/wol/wake', body),
  },
  proxy: {
    /** @returns {Promise<{setUp: boolean, running: boolean, release?: string, ports: object, upstreams: object[]}>} */
    status: () => get('/proxy/status'),
    /** @returns {Promise<object[]>} what the web application firewall matched, newest first */
    events: (params = {}) => get(withQuery('/proxy/events', params)),
  },
  wireguard: {
    /** @returns {Promise<{privateKey: string, publicKey: string}>} */
    keys: () => post('/wireguard/keys', { kind: 'pair' }),
    /** @returns {Promise<{presharedKey: string}>} */
    psk: () => post('/wireguard/keys', { kind: 'psk' }),
  },
  tailscale: {
    status: () => get('/tailscale/status'),
    /**
     * Start a login. Without a key the answer carries the URL to open.
     *
     * @param {string} [authKey]
     * @returns {Promise<{authUrl?: string, state?: string}>}
     */
    login: (authKey) => post('/tailscale/login', { authKey: authKey ?? '' }),
    logout: () => post('/tailscale/logout'),
  },
  wireless: {
    /**
     * The radios this router has, what each can do, and what it is doing.
     *
     * @returns {Promise<{setUp: boolean, country?: string, radios: object[]}>}
     */
    radios: () => get('/wireless/radios'),
    /** @returns {Promise<object[]>} */
    clients: () => get('/wireless/clients'),
  },
  update: {
    /**
     * Ask GitHub now. Admin only, and slow enough that a page should
     * prefer status() unless somebody pressed the button.
     *
     * @returns {Promise<{check: object, status: object}>}
     */
    check: (channel = 'stable') => get(`/update/check?channel=${encodeURIComponent(channel)}`),
    /**
     * What the last scheduled check found, and any install running now.
     * Nothing here reaches GitHub.
     *
     * @returns {Promise<{check: object, status: object}>}
     */
    status: () => get('/update/status'),
    apply: (channel = 'stable') => post('/update/apply', { channel }),
  },
  /**
   * The router Ostiole runs on: what it is, which daemons it has, who
   * owns its addresses, and what an older firewall left in the kernel.
   */
  host: {
    /** @returns {Promise<object>} */
    status: () => get('/host'),
    /**
     * Clear leftover rulesets. With no tables it sweeps everything with
     * no recognisable owner.
     *
     * @param {string[]} [tables] table ids from the report
     */
    flushLegacy: (tables = []) => post('/host/legacy/flush', { tables }),
  },
  systemUpdates: {
    /** What the distro package manager has waiting, and the mode in force. */
    status: () => get('/system/updates'),
    /** Ask the package manager now; this refreshes metadata, so it is slow. */
    check: () => post('/system/updates/check'),
    /**
     * Install what is waiting. Omit security to follow the configured mode.
     *
     * @param {boolean} [security] install only the security fixes
     */
    apply: (security) => post('/system/updates/apply', security === undefined ? {} : { security }),
    reboot: () => post('/system/reboot'),
  },
}
