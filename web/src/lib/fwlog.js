const KINDS = {
  'zone-drop': (e) => `${e.zone} default`,
  'default-drop': () => 'default drop',
  'block-private': () => 'private source',
  'block-bogons': () => 'bogon source',
  'block-dot': () => 'DNS over TLS',
  'block-doh': () => 'DNS over HTTPS',
  'protect-scanner': () => 'port scan',
  'protect-synflood': () => 'connection flood',
  'protect-icmpflood': () => 'ping flood',
}

/**
 * @param {object} e one entry from /api/v1/log/recent
 * @returns {string}
 */
export function matchedLabel(e) {
  if (e.kind === 'rule') return e.ruleId || '—'
  const kind = KINDS[e.kind]
  return kind ? kind(e) : e.prefix || '—'
}
