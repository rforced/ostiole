import { proxyServes } from '@/lib/proxyStatus'

/**
 * The logs the router keeps in memory, sized as the model sizes them: the
 * default and the most entries, and what one costs. They mirror the
 * model's constants, which TestTheLogsPageMirrorsTheModel checks, so a
 * page can say what a figure costs before it is applied.
 */
export const LOGS = {
  firewall: { entries: 50000, max: 10000000, bytes: 350 },
  queries: { entries: 100000, max: 10000000, bytes: 150 },
  events: { entries: 10000, max: 1000000, bytes: 1536 },
  destinations: { entries: 100000, max: 10000000, bytes: 200 },
  requests: { entries: 50000, max: 10000000, bytes: 400 },
  dhcp: { entries: 20000, max: 1000000, bytes: 300 },
  wireless: { entries: 20000, max: 1000000, bytes: 200 },
  wireguard: { entries: 10000, max: 1000000, bytes: 200 },
  tailscale: { entries: 10000, max: 1000000, bytes: 200 },
}

/** How many days an entry stays in memory, System › General: the default and the most. */
export const DAYS = { default: 7, max: 365 }

/** How many days the log files keep an entry: the default and the most. */
export const FILE_DAYS = { default: 30, max: 365 }

/** The logs in memory that System › General writes to files. */
export const FILE_LOGS = [
  'firewall',
  'queries',
  'events',
  'destinations',
  'requests',
  'dhcp',
  'wireless',
  'wireguard',
  'tailscale',
]

/** What a sentence calls each log kept in files. */
export const FILE_LOG_NAMES = {
  firewall: 'Firewall log',
  queries: 'Query log',
  events: 'WAF events',
  requests: 'Proxy requests',
  dhcp: 'DHCP log',
  wireless: 'Wireless log',
  wireguard: 'WireGuard log',
  tailscale: 'Tailscale log',
  drives: 'Drive history',
  links: 'Traffic per interface',
  devices: 'Traffic per device',
  destinations: 'Destinations',
  gateways: 'Gateway history',
  'gateway-events': 'Gateway events',
}

/**
 * How many days the log files keep an entry, 0 while they are off.
 * @param {object} [cfg]
 */
export function fileDays(cfg) {
  const files = cfg?.system?.logging?.files
  return files?.enabled ? files.retentionDays || FILE_DAYS.default : 0
}

/**
 * Whether the log level keeps what the daemons say about their clients: a
 * line per request, a lease or a wireless client. Info and Debug do.
 * @param {object} [cfg]
 */
export function records(cfg) {
  const level = cfg?.system?.logging?.level
  return level === 'info' || level === 'debug'
}

/**
 * What each log is set to in a configuration, and whether it is on. The
 * firewall log always is; the query log while it and the DNS server are
 * on; the WAF events while the proxy has something to serve, and its
 * requests while the level keeps them too; the DHCP, wireless and VPN
 * peer logs while their service is on and the level keeps them.
 * @param {object} [cfg]
 * @returns {Record<string, {on: boolean, entries: number}>}
 */
export function logSettings(cfg) {
  const firewall = cfg?.system?.management?.firewallLog ?? {}
  const dns = cfg?.services?.dns ?? {}
  const proxy = cfg?.services?.proxy ?? {}
  const traffic = cfg?.traffic ?? {}
  return {
    firewall: { on: true, entries: firewall.entries || LOGS.firewall.entries },
    destinations: {
      on: Boolean(traffic.devices && traffic.destinations?.enabled),
      entries: traffic.destinations?.entries || LOGS.destinations.entries,
    },
    queries: {
      on: Boolean(dns.enabled && dns.queryLog?.enabled),
      entries: dns.queryLog?.entries || LOGS.queries.entries,
    },
    events: { on: proxyServes(proxy), entries: proxy.events?.entries || LOGS.events.entries },
    requests: {
      on: proxyServes(proxy) && records(cfg),
      entries: proxy.requests?.entries || LOGS.requests.entries,
    },
    dhcp: {
      on: Boolean(cfg?.services?.dhcp?.enabled) && records(cfg),
      entries: cfg?.services?.dhcp?.log?.entries || LOGS.dhcp.entries,
    },
    wireless: {
      on: (cfg?.wireless?.radios ?? []).some((r) => r.enabled) && records(cfg),
      entries: cfg?.wireless?.log?.entries || LOGS.wireless.entries,
    },
    wireguard: {
      on: (cfg?.interfaces ?? []).some((i) => i.enabled && i.wireguard) && records(cfg),
      entries: cfg?.vpn?.wireguardLog?.entries || LOGS.wireguard.entries,
    },
    tailscale: {
      on: (cfg?.interfaces ?? []).some((i) => i.enabled && i.tailscale) && records(cfg),
      entries: cfg?.vpn?.tailscaleLog?.entries || LOGS.tailscale.entries,
    },
  }
}

/**
 * What a log costs full.
 * @param {string} log a key of LOGS
 * @param {number} [entries] empty is the default
 */
export function fullBytes(log, entries) {
  return (entries || LOGS[log].entries) * LOGS[log].bytes
}

/**
 * What every log that is on costs full.
 * @param {object} [cfg]
 */
export function totalBytes(cfg) {
  let sum = 0
  for (const [log, s] of Object.entries(logSettings(cfg))) {
    if (s.on) sum += fullBytes(log, s.entries)
  }
  return sum
}
