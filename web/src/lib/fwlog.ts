import type { FwlogEntry, Proxy } from '@/lib/types.gen'

export type { FwlogEntry }

/** A drop the firewall makes on its own account, led by its zone when the router named one. */
const zoned = (what: string) => (e: Omit<FwlogEntry, 'seq'>) =>
  e.zone ? `${e.zone} ${what}` : what

const KINDS: Record<string, (e: Omit<FwlogEntry, 'seq'>) => string> = {
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
 * @param e one entry from /api/v1/log/recent
 * @param access what each proxy access rule is called, by id: its description, or the id itself
 */
export function matchedLabel(
  e: Omit<FwlogEntry, 'seq'>,
  access: Record<string, string> = {},
): string {
  if (e.kind === 'rule') return e.ruleId || '—'
  if (e.kind === 'proxy') return `proxy: ${(e.ruleId && access[e.ruleId]) || e.ruleId}`
  const kind = KINDS[e.kind]
  return kind ? kind(e) : e.prefix || '—'
}

/**
 * What each access rule of the proxy is called in the log.
 * @param proxy services.proxy
 */
export function accessNames(proxy?: Pick<Proxy, 'access'> | null): Record<string, string> {
  return Object.fromEntries((proxy?.access ?? []).map((a) => [a.id, a.description || a.id]))
}

/**
 * An address and its port, as the Logs page writes one end of a packet.
 */
export function endpoint(addr?: string, port?: number) {
  if (!addr) return ''
  return port ? `${addr}:${port}` : addr
}

/**
 * The last column: TCP flags, an ICMP type, or the length.
 */
export function fwlogInfo(e: FwlogEntry): string {
  if (e.tcpFlags) return e.tcpFlags
  if (e.proto?.startsWith('icmp')) return e.icmpType != null ? `type ${e.icmpType}` : ''
  return `${e.length ?? 0} B`
}

/**
 * The values a packet's row shows, as it shows them: what the router
 * searches too (fwlog.Entry.Search).
 * @param access see accessNames
 */
export function fwlogValues(e: FwlogEntry, access: Record<string, string> = {}) {
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
