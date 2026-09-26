import { internalInterfaces } from '@/lib/interfaces'

/** What a blocking lookup comes to, in one sentence. */

/**
 * @param {{name?: string, matched?: string, reason?: string} | null} finding
 * @returns {string}
 */
export function sentence(finding) {
  const f = finding
  if (!f) return ''
  switch (f.reason) {
    case 'not-a-name':
      return 'That is not a domain name.'
    case 'allow':
      return `Not blocked: Exceptions allow ${f.matched}.`
    case 'never':
      return `Not blocked: this router answers for ${f.matched} itself.`
    case 'delegated':
      return `Not blocked: ${f.matched} is a domain override, answered by its own resolvers.`
    case 'off':
      return 'Not blocked: the DNS server is off.'
    case 'lists-off':
      return 'Not blocked: block lists are off.'
    case 'deny':
      return `Blocked: Exceptions block ${f.matched}.`
    case 'canary':
      return 'Blocked: Firefox asks this name before turning on DNS over HTTPS.'
    case 'list':
      return f.matched === f.name
        ? 'Blocked: a list has it.'
        : `Blocked: a list has ${f.matched}, which covers it.`
    default:
      return 'Not blocked: no list has it.'
  }
}

/** A name as the lists compare it: lower case, no dot at either end. */
export function normalizeName(name) {
  return String(name ?? '')
    .trim()
    .replace(/^\.+|\.+$/g, '')
    .toLowerCase()
}

/** Whether blocking parent also blocks name: blocking is by subtree. */
export function coversName(parent, name) {
  const p = normalizeName(parent)
  const n = normalizeName(name)
  return p !== '' && (p === n || n.endsWith(`.${p}`))
}

const DOMAIN_RE =
  /^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)*$/

/** Whether the check takes a name as an allow or deny entry, underscores included. */
export function isDomainName(name) {
  const n = normalizeName(name)
  return n !== '' && n.length <= 253 && DOMAIN_RE.test(n)
}

/**
 * The names this router answers for itself and the domains it has
 * delegated. Nothing at or under one is blocked, and the check refuses a
 * deny entry at or above one. Config.NeverBlocked and
 * Config.DelegatedDomains in Go.
 *
 * @param {object | null} cfg
 * @returns {string[]}
 */
export function protectedNames(cfg) {
  const dns = cfg?.services?.dns ?? {}
  const domain = normalizeName(dns.domain)
  const out = new Set()
  const add = (name) => {
    const n = normalizeName(name)
    if (n) out.add(n)
  }
  // A bare name is answered under the local domain too.
  const addBoth = (name) => {
    add(name)
    const n = String(name ?? '').trim()
    if (domain && n && !n.includes('.')) add(`${n}.${domain}`)
  }
  add(domain)
  addBoth(cfg?.system?.hostname)
  for (const h of dns.hostOverrides ?? []) {
    const own = normalizeName(h.domain)
    const under = own || domain
    for (const label of [h.hostname, ...(h.aliases ?? [])]) {
      add(under ? `${label}.${under}` : label)
      if (under && (own === '' || own === domain)) add(label)
    }
  }
  for (const l of cfg?.services?.dhcp?.staticLeases ?? []) addBoth(l.hostname)
  for (const name of proxyNames(cfg)) add(name)
  for (const d of dns.domainOverrides ?? []) add(d.domain)
  return [...out]
}

/**
 * The names the router answers for the reverse proxy's sites, while the
 * proxy and the DNS service are on and share an interface: each host but
 * a wildcard, and bare too when it is one label under the local domain.
 * Config.ProxyNames in Go.
 *
 * @param {object | null} cfg
 * @returns {string[]}
 */
function proxyNames(cfg) {
  const proxy = cfg?.services?.proxy
  const dns = cfg?.services?.dns ?? {}
  if (!proxy?.enabled || !dns.enabled) return []
  const named = dns.interfaces ?? []
  const listening = named.length
    ? (cfg.interfaces ?? []).filter((i) => i.enabled && named.includes(i.name))
    : internalInterfaces(cfg)
  const zones = new Set(proxy.zones ?? [])
  if (!listening.some((i) => zones.has(i.zone))) return []
  const domain = normalizeName(dns.domain)
  const out = []
  for (const site of proxy.sites ?? []) {
    if (!site.enabled) continue
    for (const host of (site.hosts ?? []).map(normalizeName)) {
      if (!host || host.startsWith('*.')) continue
      const label = !host.includes('.')
        ? host
        : domain && host.endsWith(`.${domain}`)
          ? host.slice(0, -domain.length - 1)
          : ''
      if (label && !label.includes('.') && domain) out.push(`${label}.${domain}`, label)
      else out.push(host)
    }
  }
  return out
}

/**
 * Which exception a query log row offers to toggle, from the draft. A name
 * already on a list offers that list, ticked. Otherwise a blocked answer
 * offers the allow list and any other the deny list, where the entry would
 * hold and pass the check.
 *
 * @param {object | null} cfg
 * @returns {(row: {name: string, status: string}) => ({key: 'allow' | 'deny', on: boolean} | null)}
 */
export function exceptionToggles(cfg) {
  const allow = (cfg?.blocking?.allow ?? []).map(normalizeName)
  const deny = new Set((cfg?.blocking?.deny ?? []).map(normalizeName))
  const guarded = protectedNames(cfg)
  return (row) => {
    const name = normalizeName(row.name)
    // Allow is asked first because it wins: a name on both is never blocked.
    if (allow.includes(name)) return { key: 'allow', on: true }
    if (deny.has(name)) return { key: 'deny', on: true }
    if (!isDomainName(name)) return null
    // The router answers it or has delegated it, so it is never blocked.
    if (guarded.some((g) => coversName(g, name))) return null
    if (row.status === 'blocked') return { key: 'allow', on: false }
    // An allowed parent beats a deny entry, and the check refuses a deny
    // entry above a name the router answers for or has delegated.
    if (allow.some((a) => coversName(a, name))) return null
    if (guarded.some((g) => coversName(name, g))) return null
    return { key: 'deny', on: false }
  }
}
