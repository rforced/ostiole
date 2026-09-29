/** Each verdict as the Events tab writes it, and its badge. */
export const VERDICTS = {
  blocked: { label: 'blocked', tone: 'badge-bad' },
  'would-block': { label: 'would block', tone: 'badge-warn' },
  matched: { label: 'matched', tone: '' },
}

/**
 * The values an event's row shows, as it shows them: what the router
 * searches too (waflog.Entry.Search).
 * @param {object} e one event
 * @returns {Array<string | number | undefined>}
 */
export function eventValues(e) {
  return [
    e.site,
    e.client,
    e.method,
    e.uri,
    e.status,
    VERDICTS[e.verdict]?.label ?? e.verdict,
    ...(e.rules ?? []).flatMap((r) => [r.id, r.message]),
  ]
}

/**
 * How long a request was open before the proxy logged it, in seconds, or 0
 * under a minute. The proxy logs a request when it ends, so a WebSocket's
 * event comes when the socket closes.
 * @param {{ time: string, logged: string }} e one event
 * @returns {number}
 */
export function openSeconds(e) {
  const seconds = (Date.parse(e.logged) - Date.parse(e.time)) / 1000
  return seconds >= 60 ? seconds : 0
}

/**
 * An event's rules, one line each however often a rule matched: a rule
 * that checks every header matches once per header, and only the first
 * match carries its message. An exclusion is of the rule, so one line
 * says all there is. count is how often it matched, and data what each
 * match found, a line each.
 * @param {object[] | null} [rules] null when the router saw none
 * @returns {object[]}
 */
export function ruleLines(rules) {
  const byId = new Map()
  for (const r of rules ?? []) {
    const line = byId.get(r.id)
    if (!line) {
      byId.set(r.id, { ...r, count: 1, data: r.data ? [r.data] : [] })
      continue
    }
    line.count++
    line.message ||= r.message
    if (r.data && !line.data.includes(r.data)) line.data.push(r.data)
  }
  return [...byId.values()].map((line) => ({ ...line, data: line.data.join('\n') }))
}
