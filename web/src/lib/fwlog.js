/** A drop the firewall makes on its own account, led by its zone when the router named one. */
const zoned = (what) => (e) => (e.zone ? `${e.zone} ${what}` : what)

const KINDS = {
  'zone-drop': (e) => `${e.zone} default`,
  'default-drop': () => 'default drop',
  'block-private': () => 'private source',
  'block-bogons': () => 'bogon source',
  'block-dot': zoned('DNS over TLS'),
  'block-doh': zoned('DNS over HTTPS'),
  'protect-scanner': zoned('port scan'),
  'protect-synflood': zoned('connection flood'),
  'protect-icmpflood': zoned('ping flood'),
}

/**
 * @param {object} e one entry from /api/v1/log/recent
 * @param {Record<string, string>} [access] what each proxy access rule is
 *   called, by id: its description, or the id itself
 * @returns {string}
 */
export function matchedLabel(e, access = {}) {
  if (e.kind === 'rule') return e.ruleId || '—'
  if (e.kind === 'proxy') return `proxy: ${access[e.ruleId] || e.ruleId}`
  const kind = KINDS[e.kind]
  return kind ? kind(e) : e.prefix || '—'
}

/**
 * What each access rule of the proxy is called in the log.
 * @param {object} [proxy] services.proxy
 * @returns {Record<string, string>}
 */
export function accessNames(proxy) {
  return Object.fromEntries((proxy?.access ?? []).map((a) => [a.id, a.description || a.id]))
}

/**
 * An address and its port, as the Logs page writes one end of a packet.
 * @param {string} [addr]
 * @param {number} [port]
 */
export function endpoint(addr, port) {
  if (!addr) return ''
  return port ? `${addr}:${port}` : addr
}

/**
 * The last column: TCP flags, an ICMP type, or the length.
 * @param {object} e
 */
export function fwlogInfo(e) {
  if (e.tcpFlags) return e.tcpFlags
  if (e.proto?.startsWith('icmp')) return e.icmpType != null ? `type ${e.icmpType}` : ''
  return `${e.length ?? 0} B`
}

/**
 * The values a packet's row shows, as it shows them: what the router
 * searches too (fwlog.Entry.Search).
 * @param {object} e one entry
 * @param {Record<string, string>} [access] see accessNames
 */
export function fwlogValues(e, access = {}) {
  return [
    e.action || 'unknown',
    matchedLabel(e, access),
    e.in,
    e.out,
    e.proto,
    endpoint(e.src, e.srcPort),
    endpoint(e.dst, e.dstPort),
    fwlogInfo(e),
  ]
}
