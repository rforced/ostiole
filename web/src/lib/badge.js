/**
 * The colour of a badge, from the word it says: whether what it reports is
 * fine. Services say running, stopped or off; links up or down; tunnels
 * connected or disconnected; the clock synchronised or not, and each time
 * server by the words SOURCE_WORDS in lib/ntpStatus.js gives it. A word not
 * here is neutral: disabled, off, offline, quiet, probing.
 */
const OK = new Set([
  'running',
  'up',
  'connected',
  'online',
  'loaded',
  'healthy',
  'active',
  'synchronised',
  'in use',
  'agrees',
  'issued',
  'current',
  'passed',
])
const WARN = new Set([
  'stopped',
  'down',
  'no carrier',
  'absent',
  'disconnected',
  'inactive',
  'missing',
  'not synchronised',
  'no answer',
  'unsteady',
  'waiting',
  'too few answers',
  'too far',
  'stale',
  'expired',
  'expires soon',
  'no address',
])
const BAD = new Set(['failed', 'failing', 'error', 'wrong time'])

/**
 * @param {string} word what the badge says
 * @returns {string} its tone's class, or '' for a neutral one
 */
export function tone(word) {
  if (OK.has(word)) return 'badge-ok'
  if (WARN.has(word)) return 'badge-warn'
  if (BAD.has(word)) return 'badge-bad'
  return ''
}

/**
 * A rule's action, coloured by the verdict: that a rule matched says
 * nothing on its own about whether the packet got through.
 * @param {string} action
 * @returns {string}
 */
export function actionTone(action) {
  if (action === 'accept') return 'badge-ok'
  if (action === 'reject') return 'badge-warn'
  if (action === 'drop') return 'badge-bad'
  return ''
}
