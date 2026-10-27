/**
 * Thin fetch wrapper for the Ostiole API.
 *
 * Every state-changing request carries the X-Requested-With header the
 * server demands (its CSRF defence). A 401 dispatches "ostiole:unauthorized"
 * on window so the router can send the user to the login page.
 */

import type {
  Account,
  Actor,
  AddPasskeyRequest,
  Applied,
  ApplyRequest,
  ApplyResult,
  AssertionResponse,
  AttestationResponse,
  AuditLogPage,
  BackupRequest,
  BlockingState,
  BlocklistRefreshReply,
  CaptureRequest,
  CatalogEntry,
  CertificateKeyReply,
  CertificateResponse,
  CertificatesResponse,
  Change,
  CheckResult,
  ClearedReply,
  Config,
  ConfigRequest,
  ConfirmReply,
  Counters,
  CreateTokenRequest,
  CreateUserRequest,
  CreatedToken,
  Credentials,
  CronRunReply,
  CronStatus,
  DDNSRecord,
  DNSProvider,
  DdnsCheckRequest,
  DdnsResponse,
  DetectedGatewayReply,
  DhcpLogPage,
  DiffRequest,
  DiscoveryAnnouncementsReply,
  DiscoveryLogPage,
  DnsCacheClearReply,
  DnsName,
  DnsblockPage,
  DnsproviderTestResult,
  Drift,
  Drive,
  DriveHistoryPage,
  DrivesResponse,
  FeedRefreshReply,
  FeedsPage,
  FeedsStatus,
  Finding,
  FlushLegacyRequest,
  FwLogPage,
  FwlogEntry,
  GatewayEvent,
  GatewayEventsPage,
  GatewayStatus,
  GatewayStrip,
  HealthResponse,
  HostReport,
  HostResult,
  IdReply,
  InspectFeedRequest,
  JournalEntry,
  Lease,
  LiveLink,
  LogLimitsInfo,
  LogfileStatus,
  LoginPageResponse,
  LoginPasskeyOptionsRequest,
  LoginPasskeyRequest,
  LoginTOTPRequest,
  Mapping,
  MfaResponse,
  ModemStatus,
  Neighbour,
  Notifications,
  NotifyStatus,
  NtpStatus,
  OpenvpnStatus,
  Overview,
  Part,
  PasskeyView,
  PasswordChange,
  PasswordRequest,
  PeerLogPage,
  PendingSignIn,
  PendingSignInReply,
  PingRequest,
  PingResult,
  Pkcs12Request,
  Plan,
  PolicyTarget,
  ProviderTestRequest,
  ProxyStatus,
  QueryLogPage,
  QuerySummary,
  RebootReply,
  RemoteCopiesResponse,
  RemoteDeleteAllRequest,
  RemoteDeleteRequest,
  RemoteRestoreRequest,
  Removed,
  RenamePasskeyRequest,
  RenameUserRequest,
  RenewRequest,
  Reply,
  RequestsPage,
  RestoreRequest,
  RestoreResponse,
  Revision,
  SelfSignedRequest,
  SelfTestRequest,
  ServerTestResult,
  ServerTrafficDestinations,
  ServicesStatus,
  SessionResponse,
  SessionRow,
  SetUserPasswordRequest,
  SetUserRoleRequest,
  SetupStatusReply,
  ShapingStatus,
  StarterRequest,
  StatesResult,
  Stats,
  StatusReply,
  SystemNAT,
  SystemRule,
  SysupdateStatus,
  TailscaleLoginReply,
  TailscaleLoginRequest,
  TailscaleStatus,
  Timezones,
  Token,
  TotpConfirmRequest,
  TotpStartReply,
  TraceRequest,
  TraceResult,
  TrafficClearReply,
  TrafficDevice,
  TrafficDevices,
  TrafficLinks,
  Tunnel,
  UnusedResponse,
  UpdateApplyRequest,
  SystemUpdateApplyRequest,
  UpdateCheckReply,
  UpdateStatus,
  UpdateStatusReply,
  WafEventsPage,
  WakeRequest,
  WireguardKeysReply,
  WireguardKeysRequest,
  WirelessClient,
  WirelessLogPage,
  WirelessRadios,
} from './types.gen'

export const UNAUTHORIZED_EVENT = 'ostiole:unauthorized'

export interface ApiIssue {
  path: string
  message: string
}

export class ApiError extends Error {
  status: number
  issues: ApiIssue[]
  /** What kind of refusal it is, e.g. "stale". */
  code: string

  constructor(status: number, message: string, issues: ApiIssue[] = [], code = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.issues = issues
    this.code = code
  }
}

/** Endpoints where a 401 means "wrong credentials", not "session expired". */
const CREDENTIAL_PATHS = [
  '/auth/password',
  '/auth/mfa/clear',
  '/auth/mfa/passkeys/options',
  '/setup',
]

/** The sign-in steps answer a wrong code or an ended sign-in with a 401 too. */
const credentialPath = (path: string) =>
  path.startsWith('/auth/login') || CREDENTIAL_PATHS.includes(path)

/** Resolves to a response that is ok; the signal ends the request early. */
async function send(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<Response> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'X-Requested-With': 'ostiole',
  }
  const init: RequestInit = { method, headers, credentials: 'same-origin', signal }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const res = await fetch(`/api/v1${path}`, init)
  if (res.status === 401 && !credentialPath(path)) {
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path } }))
  }
  if (!res.ok) throw await apiError(res)
  return res
}

async function request<T = unknown>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const res = await send(method, path, body, signal)
  if (res.status === 204) return undefined as T
  const type = res.headers.get('Content-Type') ?? ''
  if (type.startsWith('text/')) return (await res.text()) as T
  return res.json()
}

const get = <T = unknown>(path: string, signal?: AbortSignal) =>
  request<T>('GET', path, undefined, signal)
const post = <T = unknown>(path: string, body?: unknown) => request<T>('POST', path, body)

/**
 * Appends the filter parameters that are actually set. An empty one means
 * "any", and sending it would narrow the answer to rows with an empty
 * field.
 */
function withQuery(path: string, params: object = {}) {
  const q = new URLSearchParams(
    Object.entries(params)
      .filter(([, v]) => v !== '' && v !== undefined && v !== null)
      .map(([k, v]) => [k, String(v)]),
  )
  const s = q.toString()
  return s ? `${path}?${s}` : path
}

/** Turns a failed response into an ApiError, for the calls that fetch directly. */
async function apiError(res: Response) {
  let message = res.statusText || `HTTP ${res.status}`
  let issues: ApiIssue[] = []
  let code = ''
  try {
    const err = await res.json()
    if (err?.error) message = err.error
    if (Array.isArray(err?.issues)) issues = err.issues
    if (typeof err?.code === 'string') code = err.code
  } catch {
    /* non-JSON error body */
  }
  return new ApiError(res.status, message, issues, code)
}

/**
 * Base64 for a JSON body, chunked so a large file does not blow the
 * argument limit of String.fromCharCode.
 */
function base64(buf: ArrayBuffer) {
  const bytes = new Uint8Array(buf)
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  }
  return btoa(s)
}

export type {
  Account,
  Actor,
  ApplyResult,
  Applied,
  AuditEvent,
  AuditLogPage,
  Change,
  Config,
  CreatedToken,
  Drift,
  DriftChange,
  MfaResponse,
  PasskeyView,
  PendingSignIn,
  Plan,
  Revision,
  Role,
  SessionResponse,
  SessionRow,
  StatusReply,
  SystemNAT,
  SystemRule,
  Token,
} from './types.gen'

export type Session = SessionResponse
export type Status = StatusReply
export type PendingLogin = PendingSignIn
export type AuditActor = Actor
export type Mfa = MfaResponse
export type Passkey = PasskeyView
export type SystemNat = SystemNAT

/** Filter parameters of a list read; the unset ones are left out. */
type Query = object

/** One probe set of a gateway's history: [seconds, mean, low, high, loss], the latency null where nothing answered (gateway.Point has a custom MarshalJSON). */
export type GatewayHistoryPoint = [number, number | null, number | null, number | null, number]

/** GET /gateways/{name}/history is untyped in the generated file because of GatewayHistoryPoint. */
export interface GatewayHistoryReply {
  window: string
  now: string
  families: {
    family: string
    points: GatewayHistoryPoint[]
    mean: number
    worst: number
    loss: number
    worstLoss: number
    sent: number
  }[]
  down: number
  never: number
  monitor?: string
  events: GatewayEvent[]
}

/** The `publicKey` map is open in the generated types; the browser's own JSON types describe it. */
export interface PasskeyLoginStart {
  token: string
  publicKey: PublicKeyCredentialRequestOptionsJSON
}

export const api = {
  health: () => get<HealthResponse>('/health'),
  setup: {
    status: () => get<SetupStatusReply>('/setup'),
    create: (username: string, password: string) =>
      post<SessionResponse>('/setup', { username, password } satisfies Credentials),
  },
  auth: {
    /**
     * What the sign-in page shows: the router's hostname, and whether an
     * account has a passkey made on this host name.
     */
    loginPage: () => get<LoginPageResponse>('/auth/login'),
    /**
     * A sign-in with a passkey alone: its token and a request that names no
     * account, so the browser offers each passkey it keeps for this host name.
     */
    loginStart: () => post<PasskeyLoginStart>('/auth/login/start'),
    login: (username: string, password: string) =>
      post<SessionResponse | PendingSignInReply>('/auth/login', {
        username,
        password,
      } satisfies Credentials),
    loginTOTP: (pending: string, code: string) =>
      post<SessionResponse>('/auth/login/totp', { pending, code } satisfies LoginTOTPRequest),
    passkeyLoginOptions: (pending: string) =>
      post<{ publicKey: PublicKeyCredentialRequestOptionsJSON }>('/auth/login/passkey/options', {
        pending,
      } satisfies LoginPasskeyOptionsRequest),
    loginPasskey: (pending: string, credential: AssertionResponse) =>
      post<SessionResponse>('/auth/login/passkey', {
        pending,
        credential,
      } satisfies LoginPasskeyRequest),
    logout: () => post<void>('/auth/logout'),
    me: () => get<SessionResponse>('/auth/me'),
    changePassword: (current: string, next: string) =>
      post<SessionResponse>('/auth/password', { current, new: next } satisfies PasswordChange),
    /**
     * What the signed-in account has for multi-factor authentication, never the secrets.
     */
    mfa: () => get<MfaResponse>('/auth/mfa'),
    /**
     * A new authenticator secret, kept until a code from it is confirmed.
     */
    totpStart: () => post<TotpStartReply>('/auth/mfa/totp/start'),
    totpConfirm: (code: string) =>
      post<void>('/auth/mfa/totp/confirm', { code } satisfies TotpConfirmRequest),
    /** Removes the authenticator app and every passkey, on the password. */
    mfaClear: (password: string) =>
      post<void>('/auth/mfa/clear', { password } satisfies PasswordRequest),
    /**
     * The challenge for a new passkey, on the password: a wrong one is
     * refused before the browser's prompt.
     */
    passkeyOptions: (password: string) =>
      post<{ publicKey: PublicKeyCredentialCreationOptionsJSON }>('/auth/mfa/passkeys/options', {
        password,
      } satisfies PasswordRequest),
    addPasskey: (name: string, credential: AttestationResponse) =>
      post<PasskeyView>('/auth/mfa/passkeys', { name, credential } satisfies AddPasskeyRequest),
    renamePasskey: (id: string, name: string) =>
      request<PasskeyView>('PATCH', `/auth/mfa/passkeys/${encodeURIComponent(id)}`, {
        name,
      } satisfies RenamePasskeyRequest),
    removePasskey: (id: string) =>
      request<void>('DELETE', `/auth/mfa/passkeys/${encodeURIComponent(id)}`),
    sessions: () => get<SessionRow[]>('/auth/sessions'),
    endSession: (id: string) => request<void>('DELETE', `/auth/sessions/${encodeURIComponent(id)}`),
    endOtherSessions: () => post<void>('/auth/sessions/end-others'),
    /** The signed-in account's own API tokens. */
    tokens: {
      list: () => get<Token[]>('/auth/tokens'),
      create: (body: CreateTokenRequest) => post<CreatedToken>('/auth/tokens', body),
      remove: (id: string) => request<void>('DELETE', `/auth/tokens/${encodeURIComponent(id)}`),
    },
  },
  status: () => get<StatusReply>('/status'),
  /** Everything the dashboard shows, in one request. */
  overview: () => get<Overview>('/overview'),
  /**
   * CPU, memory and disk. The CPU figure covers the time since the previous
   * request, so the first one after a restart has none.
   */
  systemStats: () => get<Stats>('/system/stats'),
  /** The zones this router can be set to, and the one its clock reads now. */
  timezones: () => get<Timezones>('/system/timezones'),
  /** Whether the logs are written to files, what the files hold, and how writing goes. */
  logFiles: () => get<LogfileStatus>('/system/log-files'),
  /**
   * What this router's memory allows the logs: their budget at their
   * largest, the peak factor, and the most entries each log may keep.
   */
  logLimits: () => get<LogLimitsInfo>('/logs/limits'),
  /** Empties every log a page can clear and deletes its files. */
  clearLogs: () => request<ClearedReply>('DELETE', '/system/logs'),
  config: {
    /**
     * The saved configuration and its revision, which an apply names as
     * the one its draft was read from.
     */
    get: async (): Promise<{ config: Config; revision: string }> => {
      const res = await send('GET', '/config')
      const etag = res.headers.get('ETag') ?? ''
      return { config: (await res.json()) as Config, revision: etag.replace(/^(W\/)?"|"$/g, '') }
    },
    /** Build (without saving) a first configuration from the wizard answers. */
    starter: (opts: StarterRequest) => post<Config>('/config/starter', opts),
    revisions: () => get<Revision[]>('/config/revisions'),
    revision: (id: string) => get<Config>(`/config/revisions/${encodeURIComponent(id)}`),
    /** Who applied the configuration in force, or null when nothing recorded it. Admins only. */
    applied: () => get<Applied | null>('/config/applied'),
    check: (config: Config) => post<Plan>('/check', { config } satisfies ConfigRequest),
    /**
     * The server refuses it with a 409 and code "stale" once baseRevision is
     * not the saved one. The status repeats tag while the apply awaits
     * confirmation.
     */
    apply: (config: Config, baseRevision: string, confirmTimeoutSeconds = 60, tag = '') =>
      post<ApplyResult>('/apply', {
        config,
        confirmTimeoutSeconds,
        baseRevision,
        ...(tag ? { tag } : {}),
      } satisfies ApplyRequest),
    confirm: () => post<ConfirmReply>('/apply/confirm'),
    revert: () => post<void>('/apply/revert'),
    /**
     * The lines applying the saved configuration again would change.
     */
    drift: () => get<Drift>('/apply/drift'),
    /** Compares two configurations; each side is a revision id, 'current', or inline. */
    diff: (body: DiffRequest) => post<Change[]>('/config/diff', body),
    /** What a configuration leaves unused or switched off. */
    unused: (config: Config) => post<UnusedResponse>('/config/unused', config),
    /**
     * Downloads a backup; returns the file and the name the server chose.
     * It is a POST because a passphrase has no business in a URL.
     */
    backup: async ({
      users = false,
      note = '',
      passphrase = '',
      redact = false,
    }: BackupRequest = {}) => {
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
    remoteCopies: () => get<RemoteCopiesResponse>('/config/backup/remote'),
    /** Reads one backup out of the bucket and reports what it would change. */
    restoreRemote: (key: string, passphrase = '') =>
      post<RestoreResponse>('/config/backup/remote/restore', {
        key,
        passphrase,
      } satisfies RemoteRestoreRequest),
    /** Deletes one copy from the bucket, every version of it. */
    deleteRemote: (key: string) =>
      post<Removed>('/config/backup/remote/delete', { key } satisfies RemoteDeleteRequest),
    /** Deletes those copies, and every hidden one in the folder. Admin only. */
    deleteAllRemote: (keys: string[]) =>
      post<Removed>('/config/backup/remote/delete-all', { keys } satisfies RemoteDeleteAllRequest),
    /** Parses an uploaded backup and reports what it would change. */
    restore: async (file: File, passphrase = ''): Promise<RestoreResponse> => {
      const data = base64(await file.arrayBuffer())
      const res = await fetch('/api/v1/config/restore', {
        method: 'POST',
        headers: { 'X-Requested-With': 'ostiole', 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ data, passphrase } satisfies RestoreRequest),
      })
      if (!res.ok) throw await apiError(res)
      return res.json()
    },
  },
  ruleset: () => get<string>('/ruleset'),
  counters: () => get<Counters>('/counters'),
  /** The rules Ostiole adds on its own for a configuration, in evaluation order. */
  systemRules: (config: Config) =>
    post<SystemRule[]>('/rules/system', { config } satisfies ConfigRequest),
  /** The outbound NAT rules a configuration makes Ostiole write on its own: the automatic masquerade. */
  systemNat: (config: Config) =>
    post<SystemNAT[]>('/nat/system', { config } satisfies ConfigRequest),
  /** The names a configuration makes the DNS server answer on its own: static leases with hostnames. */
  dnsNames: (config: Config) => post<DnsName[]>('/dns/names', { config } satisfies ConfigRequest),
  /** Empty the caches the router is running now. Answers with what it cleared. */
  clearDnsCache: () => request<DnsCacheClearReply>('DELETE', '/dns/cache'),
  interfaces: {
    live: () => get<LiveLink[]>('/interfaces/live'),
    /** Ask for a fresh lease; release drops the current one first. */
    renew: (name: string, release = false) =>
      post<void>(`/interfaces/${encodeURIComponent(name)}/renew`, {
        release,
      } satisfies RenewRequest),
  },
  /** Live gateway health from the multi-WAN monitor. */
  gateways: () => get<GatewayStatus[]>('/gateways'),
  /** Default routes the kernel already has, with the gateway each would become. */
  detectedGateways: () => get<DetectedGatewayReply[]>('/gateways/detected'),
  /** Who did what on the router. Admins only. */
  audit: {
    /** A page of entries: {q, before, limit}. */
    log: (params: Query = {}, signal?: AbortSignal) =>
      get<AuditLogPage>(withQuery('/audit', params), signal),
    /** Empties it but for one entry saying who cleared it. */
    clear: () => request<ClearedReply>('DELETE', '/audit'),
  },
  /** What each gateway's probes found over a window, and what changed. */
  gatewayHistory: {
    read: (name: string, window: string) =>
      get<GatewayHistoryReply>(
        `/gateways/${encodeURIComponent(name)}/history?window=${encodeURIComponent(window)}`,
      ),
    /** Each watched gateway's last day in ten-minute cells. */
    strips: () => get<GatewayStrip[]>('/gateways/strips'),
    /** A page of events: {gateway, q, before, limit}. */
    events: (params: Query = {}, signal?: AbortSignal) =>
      get<GatewayEventsPage>(withQuery('/gateways/events', params), signal),
    /** Forgets every gateway's latency and loss; the events stay. */
    clear: () => request<ClearedReply>('DELETE', '/gateways/history'),
    clearEvents: () => request<ClearedReply>('DELETE', '/gateways/events'),
  },
  policy: () => get<PolicyTarget[]>('/policy'),
  policyReplies: () => get<Reply[]>('/policy/replies'),
  /** Line speeds per interface and how the queues behind them are doing. */
  shaping: () => get<ShapingStatus>('/shaping'),
  diagnostics: {
    ping: (body: PingRequest) => post<PingResult>('/diagnostics/ping', body),
    traceroute: (body: TraceRequest) => post<TraceResult>('/diagnostics/traceroute', body),
    journal: (params: Query = {}) => get<JournalEntry[]>(withQuery('/diagnostics/journal', params)),
    states: (params: Query = {}) => get<StatesResult>(withQuery('/diagnostics/states', params)),
    neighbours: () => get<Neighbour[]>('/diagnostics/neighbours'),
    /** What the cable modem reports; refresh reads it again instead of the minute-old copy. */
    modem: (address: string, refresh = false) => {
      const q = new URLSearchParams({ address })
      if (refresh) q.set('refresh', '1')
      return get<ModemStatus>(`/diagnostics/modem?${q}`)
    },
    /** Every drive in this router, with the tool's version and whether we are root. */
    drives: () => get<DrivesResponse>('/diagnostics/drives'),
    /** A page of what the hourly check read on each drive, as a log page is. */
    driveHistory: (params: Query = {}, signal?: AbortSignal) =>
      get<DriveHistoryPage>(withQuery('/diagnostics/drives/history', params), signal),
    clearDriveHistory: () => request<ClearedReply>('DELETE', '/diagnostics/drives/history'),
    drive: (name: string) => get<Drive>(`/diagnostics/drives/${encodeURIComponent(name)}`),
    /** Starts a self-test and answers with the drive as it reads afterwards. */
    selfTest: (name: string, kind: string) =>
      post<Drive>(`/diagnostics/drives/${encodeURIComponent(name)}/self-test`, {
        kind,
      } satisfies SelfTestRequest),
    abortSelfTest: (name: string) =>
      request<Drive>('DELETE', `/diagnostics/drives/${encodeURIComponent(name)}/self-test`),
    /** Downloads one drive's full report; returns the blob and the name the server chose. */
    driveReport: async (name: string) => {
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
    capture: async (body: CaptureRequest) => {
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
    recent: (limit = 200) => get<FwlogEntry[]>(`/log/recent?limit=${limit}`),
    /** A page of the firewall log: {entries, next, more, searchedTo, held, oldest}. */
    entries: (params: Query = {}, signal?: AbortSignal) =>
      get<FwLogPage>(withQuery('/log/entries', params), signal),
    /** Empties the firewall log and deletes its files. */
    clear: () => request<ClearedReply>('DELETE', '/log'),
  },
  aliases: {
    feeds: () => get<FeedsStatus[]>('/aliases/feeds'),
    refresh: (name: string) =>
      post<FeedRefreshReply>(`/aliases/${encodeURIComponent(name)}/refresh`),
    refreshAll: () => post<FeedsStatus[]>('/aliases/feeds/refresh'),
    /** Reads a list no alias names yet: its size and what it can be narrowed by. */
    inspect: (url: string) => post<Part>('/aliases/inspect', { url } satisfies InspectFeedRequest),
    /** A page of what a fetched alias holds: q, offset and limit. */
    entries: (name: string, params: Query = {}) =>
      get<FeedsPage>(withQuery(`/aliases/${encodeURIComponent(name)}/entries`, params)),
  },
  blocking: {
    status: () => get<BlockingState>('/blocking'),
    catalog: () => get<CatalogEntry[]>('/blocking/catalog'),
    lookup: (name: string) => get<Finding>(`/blocking/lookup?name=${encodeURIComponent(name)}`),
    refresh: (name: string) =>
      post<BlocklistRefreshReply>(`/blocking/lists/${encodeURIComponent(name)}/refresh`),
    refreshAll: () => post<BlockingState>('/blocking/refresh'),
    /** A page of the names a list holds: q, offset and limit. */
    names: (name: string, params: Query = {}) =>
      get<DnsblockPage>(withQuery(`/blocking/lists/${encodeURIComponent(name)}/names`, params)),
  },
  /** What each link and, while counting is on, each device moved over a window. */
  traffic: {
    interfaces: (window: string) => get<TrafficLinks>(`/traffic/interfaces?window=${window}`),
    devices: (window: string) => get<TrafficDevices>(`/traffic/devices?window=${window}`),
    device: (id: string, window: string) =>
      get<TrafficDevice>(`/traffic/devices/${encodeURIComponent(id)}?window=${window}`),
    /** A page of destinations: {window, device, q, offset, limit}. */
    destinations: (params: Query = {}) =>
      get<ServerTrafficDestinations>(withQuery('/traffic/destinations', params)),
    /** Forgets every device and what it moved, and every destination. */
    clear: () => request<TrafficClearReply>('DELETE', '/traffic'),
    /** Forgets every destination and leaves the devices. */
    clearDestinations: () => request<ClearedReply>('DELETE', '/traffic/destinations'),
    /** Forgets what every link moved, and its errors. */
    clearInterfaces: () => request<ClearedReply>('DELETE', '/traffic/interfaces'),
  },
  /** What the DNS server answered, while the query log is on. */
  queries: {
    list: (params: Query = {}, signal?: AbortSignal) =>
      get<QueryLogPage>(withQuery('/dns/queries', params), signal),
    summary: () => get<QuerySummary>('/dns/queries/summary'),
    clear: () => request<ClearedReply>('DELETE', '/dns/queries'),
  },
  crons: {
    list: () => get<CronStatus[]>('/crons'),
    run: (id: string) => post<CronRunReply>(`/crons/${encodeURIComponent(id)}/run`),
  },
  notifications: {
    /** How mail and the webhook have done, and what went out lately. */
    status: () => get<NotifyStatus>('/notifications'),
    /** Sends a test to the targets these settings switch on. */
    test: (settings: Notifications) => post<ServerTestResult[]>('/notifications/test', settings),
  },
  users: {
    list: () => get<Account[]>('/users'),
    create: (body: CreateUserRequest) => post<Account[]>('/users', body),
    remove: (username: string) =>
      request<Account[]>('DELETE', `/users/${encodeURIComponent(username)}`),
    setRole: (username: string, role: string) =>
      post<Account[]>(`/users/${encodeURIComponent(username)}/role`, {
        role,
      } satisfies SetUserRoleRequest),
    setPassword: (username: string, password: string) =>
      post<void>(`/users/${encodeURIComponent(username)}/password`, {
        password,
      } satisfies SetUserPasswordRequest),
    rename: (username: string, next: string) =>
      post<Account[]>(`/users/${encodeURIComponent(username)}/username`, {
        username: next,
      } satisfies RenameUserRequest),
    /** An admin takes another account's authenticator app and passkeys away. */
    clearMFA: (username: string) =>
      request<Account[]>('DELETE', `/users/${encodeURIComponent(username)}/mfa`),
  },
  /** Every account's tokens and the router's, for an admin. New ones are made under auth.tokens. */
  tokens: {
    list: () => get<Token[]>('/tokens'),
    remove: (id: string) => request<void>('DELETE', `/tokens/${encodeURIComponent(id)}`),
  },
  certificates: {
    list: () => get<CertificatesResponse>('/certificates'),
    regenerate: (hosts?: string[]) =>
      post<CertificateResponse>(
        '/certificates/self-signed',
        (hosts ? { hosts } : {}) satisfies SelfSignedRequest,
      ),
    /** A fresh account key, so one never has to be pasted in. */
    key: () => post<CertificateKeyReply>('/certificates/keys', {}),
    issue: (id: string) => post<IdReply>(`/certificates/${encodeURIComponent(id)}/issue`, {}),
    fileURL: (id: string, name: string) =>
      `/api/v1/certificates/${encodeURIComponent(id)}/files/${encodeURIComponent(name)}`,
    /** Downloads a PKCS#12 bundle; returns the blob and the name the server chose. */
    pkcs12: async (id: string, password: string) => {
      const res = await fetch(`/api/v1/certificates/${encodeURIComponent(id)}/pkcs12`, {
        method: 'POST',
        headers: { 'X-Requested-With': 'ostiole', 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ password } satisfies Pkcs12Request),
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
    status: () => get<ServicesStatus>('/services/status'),
    leases: () => get<Lease[]>('/dhcp/leases'),
    /** A page of what the DHCP server said of its clients, and whether the level keeps it. */
    dhcpLog: (params: Query = {}, signal?: AbortSignal) =>
      get<DhcpLogPage>(withQuery('/dhcp/log', params), signal),
    clearDhcpLog: () => request<ClearedReply>('DELETE', '/dhcp/log'),
    /** A page of the packets the discovery relay saw, relayed or dropped. */
    discoveryLog: (params: Query = {}, signal?: AbortSignal) =>
      get<DiscoveryLogPage>(withQuery('/discovery/log', params), signal),
    clearDiscoveryLog: () => request<ClearedReply>('DELETE', '/discovery/log'),
    /** What devices said they offer. */
    discoveryAnnouncements: (signal?: AbortSignal) =>
      get<DiscoveryAnnouncementsReply>('/discovery/announcements', signal),
  },
  upnp: {
    mappings: () => get<Mapping[]>('/upnp/mappings'),
  },
  ntp: {
    /** Whether the clock follows a server, the servers, and the defaults. */
    status: () => get<NtpStatus>('/ntp/status'),
  },
  wol: {
    wake: (body: WakeRequest) => post<void>('/wol/wake', body),
  },
  dnsProviders: {
    /**
     * Tries a provider's credentials as the dialog holds them and reads each
     * of its domains. Nothing is written.
     */
    test: (provider: DNSProvider) =>
      post<DnsproviderTestResult>('/dns-providers/test', {
        provider,
      } satisfies ProviderTestRequest),
  },
  ddns: {
    /** Each record and type in force, and the provider kinds that can keep one. */
    status: () => get<DdnsResponse>('/ddns'),
    /** Asks the provider about a record now; the status shows how it went. */
    update: (id: string) => post<IdReply>(`/ddns/${encodeURIComponent(id)}/update`, {}),
    /**
     * What an apply would do with a record, read from the provider with the
     * draft's credentials. Nothing is written. One result per record type.
     */
    check: (config: Config, record: DDNSRecord) =>
      post<CheckResult[]>('/ddns/check', { config, record } satisfies DdnsCheckRequest),
  },
  proxy: {
    status: () => get<ProxyStatus>('/proxy/status'),
    /** A page of what the web application firewall matched, as a log page is. */
    events: (params: Query = {}, signal?: AbortSignal) =>
      get<WafEventsPage>(withQuery('/proxy/events', params), signal),
    clearEvents: () => request<ClearedReply>('DELETE', '/proxy/events'),
    /** A page of what the proxy answered, as a log page is, and whether the level keeps them. */
    requests: (params: Query = {}, signal?: AbortSignal) =>
      get<RequestsPage>(withQuery('/proxy/requests', params), signal),
    clearRequests: () => request<ClearedReply>('DELETE', '/proxy/requests'),
  },
  wireguard: {
    keys: () =>
      post<WireguardKeysReply>('/wireguard/keys', {
        kind: 'pair',
      } satisfies Partial<WireguardKeysRequest>),
    psk: () =>
      post<WireguardKeysReply>('/wireguard/keys', {
        kind: 'psk',
      } satisfies Partial<WireguardKeysRequest>),
    publicKey: (privateKey: string) =>
      post<WireguardKeysReply>('/wireguard/keys', {
        kind: 'public',
        privateKey,
      } satisfies Partial<WireguardKeysRequest>),
    /** Each tunnel of the running configuration as its device has it. */
    status: () => get<Tunnel[]>('/wireguard/status'),
    /** A page of the peers connecting and going quiet, and whether the level keeps them. */
    log: (params: Query = {}, signal?: AbortSignal) =>
      get<PeerLogPage>(withQuery('/wireguard/log', params), signal),
    clearLog: () => request<ClearedReply>('DELETE', '/wireguard/log'),
  },
  tailscale: {
    status: () => get<TailscaleStatus>('/tailscale/status'),
    /** A page of the peers going online and offline, and whether the level keeps them. */
    log: (params: Query = {}, signal?: AbortSignal) =>
      get<PeerLogPage>(withQuery('/tailscale/log', params), signal),
    clearLog: () => request<ClearedReply>('DELETE', '/tailscale/log'),
    /** Start a login. Without a key the answer carries the URL to open. */
    login: (authKey?: string) =>
      post<TailscaleLoginReply>('/tailscale/login', {
        authKey: authKey ?? '',
      } satisfies TailscaleLoginRequest),
    logout: () => post<TailscaleStatus>('/tailscale/logout'),
  },
  openvpn: {
    /** Each configured tunnel as its daemon has it, and whether this router can run one. */
    status: () => get<OpenvpnStatus>('/openvpn/status'),
  },
  wireless: {
    /** The radios this router has, what each can do, and what it is doing. */
    radios: () => get<WirelessRadios>('/wireless/radios'),
    clients: () => get<WirelessClient[]>('/wireless/clients'),
    /** A page of clients joining and leaving, and whether the level keeps them. */
    log: (params: Query = {}, signal?: AbortSignal) =>
      get<WirelessLogPage>(withQuery('/wireless/log', params), signal),
    clearLog: () => request<ClearedReply>('DELETE', '/wireless/log'),
  },
  update: {
    /**
     * Ask GitHub now. Admin only, and slow enough that a page should
     * prefer status() unless somebody pressed the button.
     */
    check: (channel = 'stable') =>
      get<UpdateCheckReply>(`/update/check?channel=${encodeURIComponent(channel)}`),
    /**
     * What the last scheduled check found, and any install running now.
     * Nothing here reaches GitHub.
     */
    status: () => get<UpdateStatusReply>('/update/status'),
    apply: (channel = 'stable') =>
      post<UpdateStatus>('/update/apply', { channel } satisfies UpdateApplyRequest),
  },
  /**
   * The router Ostiole runs on: what it is, which daemons it has, who
   * owns its addresses, and what an older firewall left in the kernel.
   */
  host: {
    status: () => get<HostReport>('/host'),
    /**
     * Clear leftover rulesets. With no tables it sweeps everything with
     * no recognisable owner. Tables are ids from the report.
     */
    flushLegacy: (tables: string[] = []) =>
      post<HostResult>('/host/legacy/flush', { tables } satisfies FlushLegacyRequest),
  },
  systemUpdates: {
    /** What the distro package manager has waiting, and the mode in force. */
    status: () => get<SysupdateStatus>('/system/updates'),
    /** Ask the package manager now; this refreshes metadata, so it is slow. */
    check: () => post<SysupdateStatus>('/system/updates/check'),
    /** Install what is waiting. Omit either to follow the configuration. */
    apply: (security?: boolean, exclude?: string[]) =>
      post<SysupdateStatus>('/system/updates/apply', {
        security,
        exclude,
      } satisfies Partial<SystemUpdateApplyRequest>),
    reboot: () => post<RebootReply>('/system/reboot'),
  },
}
