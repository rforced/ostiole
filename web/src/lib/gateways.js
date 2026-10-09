import { SPANS } from '@/lib/chart'
import { formatDuration, formatMs } from '@/lib/format'

/** How often a gateway is probed unless it says, in seconds (model.DefaultProbeSeconds). */
export const DEFAULT_PROBE_SECONDS = 30

/** Seconds a point covers in a day's and a month's window: a minute, an hour. */
export const STEPS = { '24h': 60, '31d': 3600 }

/**
 * Seconds a point covers in a window: over five minutes, one probe.
 * @param {string} window
 * @param {number} [every] the gateway's seconds between probes
 */
export function stepOf(window, every) {
  return window === '5m' ? every || DEFAULT_PROBE_SECONDS : STEPS[window]
}

/** The families a gateway is probed in, in the order they are charted. */
export const FAMILIES = ['IPv4', 'IPv6']

/** The chart's colours, a family each: Traffic's two. */
const COLORS = { IPv4: 'var(--chart-down)', IPv6: 'var(--chart-up)' }

/** What each kind of event says, as the router searches it (gateway.eventWords). */
export const EVENT_WORDS = {
  down: 'down',
  up: 'up again',
  never: 'never answered',
  answered: 'answered',
  'family-down': 'stopped answering',
  'family-up': 'answering again',
  'family-never': 'never answered',
  monitor: 'monitor changed',
  slow: 'slow',
  'slow-end': 'no longer slow',
  lossy: 'losing packets',
  'lossy-end': 'no longer losing packets',
}

/**
 * What an event's row says happened.
 * @param {object} e
 */
export function eventText(e) {
  switch (e.kind) {
    case 'down':
      return 'Down'
    case 'up':
      return 'Up again'
    case 'never':
      return 'Never answered'
    case 'answered':
      return 'Answered'
    case 'family-down':
      return `${e.family} stopped answering`
    case 'family-up':
      return `${e.family} answering again`
    case 'family-never':
      return `${e.family} never answered`
    case 'monitor':
      return `Monitor changed to ${e.monitor || 'the next hop'}, was ${e.was || 'the next hop'}`
    case 'slow':
      return `${e.family} slow, ${formatMs(e.latencyMs)} on average, above ${e.limit} ms`
    case 'slow-end':
      return `${e.family} no longer slow`
    case 'lossy':
      return `${e.family} losing packets, ${Math.round(e.lossPercent)}% lost, above ${e.limit}%`
    case 'lossy-end':
      return `${e.family} no longer losing packets`
  }
  return e.kind
}

/**
 * The values an event's row shows, as the router searches them.
 * @param {object} e
 */
export function eventValues(e) {
  return [e.gateway, e.family, EVENT_WORDS[e.kind], e.error, e.monitor, e.was]
}

/**
 * How long the state an event ended lasted: 4m, 1h 2m.
 * @param {object} e
 */
export function eventFor(e) {
  return e.for ? formatDuration(e.for) : ''
}

/**
 * What a gateway's figures are measured to: its monitor, or its next hops.
 * @param {string} [monitor]
 * @param {number} [families] how many families it is probed in
 */
export function measured(monitor, families = 1) {
  if (monitor) return `to ${monitor}`
  return families > 1 ? 'to both next hops' : 'to the next hop'
}

/**
 * What the monitor made of a gateway, or of one family of it.
 * @param {{online?: boolean, unknown?: boolean, neverAnswered?: boolean}} s
 */
export function stateOf(s) {
  if (s.unknown) return 'probing'
  if (s.online) return 'up'
  if (s.neverAnswered) return 'never answered'
  return 'down'
}

/**
 * What to do about a gateway that never answered.
 * @param {{monitor?: string}} g the configured gateway
 */
export function neverHint(g) {
  return g.monitor
    ? 'Check the monitor address, or remove the gateway.'
    : 'Set a monitor address, or remove the gateway.'
}

/**
 * The chart's rows from a history report: [t, mean, low, high] for each
 * family, then each family's share lost. A family with nothing at a moment
 * is undefined there, so its line goes on; one that went unanswered is
 * null, so its line breaks.
 * @param {{families: Array<{family: string, points: Array<Array<number|null>>}>}} report
 */
export function historyRows(report) {
  const fams = FAMILIES.filter((f) => report.families.some((r) => r.family === f))
  const width = 1 + fams.length * 4
  const rows = new Map()
  fams.forEach((family, k) => {
    const r = report.families.find((x) => x.family === family)
    for (const [t, mean, low, high, loss] of r.points) {
      let row = rows.get(t)
      if (!row) {
        row = Array(width).fill(undefined)
        row[0] = t
        rows.set(t, row)
      }
      row[1 + k * 3] = mean
      row[2 + k * 3] = low
      row[3 + k * 3] = high
      row[1 + fams.length * 3 + k] = loss
    }
  })
  return { families: fams, rows: [...rows.values()].sort((a, b) => a[0] - b[0]) }
}

/**
 * The lines and bars that chart those rows.
 * @param {string[]} families
 */
export function historySeries(families) {
  return {
    series: families.map((f, k) => ({
      index: 1 + k * 3,
      name: f,
      color: COLORS[f],
      band: [2 + k * 3, 3 + k * 3],
    })),
    bars: families.map((f, k) => ({
      index: 1 + families.length * 3 + k,
      name: `${f} lost`,
      color: COLORS[f],
    })),
  }
}

/**
 * Rows with a probe added, as the stream sends it, and those older than
 * the window let go.
 * @param {Array<Array<number|null|undefined>>} rows
 * @param {string[]} families
 * @param {{family: string, time: string, latencyMs?: number, lost?: boolean}} p
 * @param {string} window
 */
export function addProbe(rows, families, p, window) {
  const k = families.indexOf(p.family)
  if (k < 0) return rows
  const t = Math.round(Date.parse(p.time) / 1000)
  const ms = p.lost ? null : (p.latencyMs ?? 0)
  const cut = t - SPANS[window]
  const kept = rows.filter((r) => r[0] >= cut)
  let row = kept.find((r) => r[0] === t)
  if (!row) {
    row = Array(1 + families.length * 4).fill(undefined)
    row[0] = t
    kept.push(row)
  }
  row[1 + k * 3] = ms
  row[2 + k * 3] = ms
  row[3 + k * 3] = ms
  row[1 + families.length * 3 + k] = p.lost ? 100 : 0
  return kept
}

/**
 * A family's figures over rows at five minutes: the mean and worst round
 * trip of what answered, and the share lost.
 * @param {Array<Array<number|null|undefined>>} rows
 * @param {number} k the family's place
 * @param {number} count how many families
 */
export function probeFigures(rows, k, count) {
  let sent = 0
  let lost = 0
  let sum = 0
  let worst = 0
  for (const r of rows) {
    const v = r[1 + k * 3]
    if (v === undefined) continue
    sent++
    if (v === null || r[1 + count * 3 + k] === 100) {
      lost++
      continue
    }
    sum += v
    worst = Math.max(worst, v)
  }
  const answered = sent - lost
  return {
    sent,
    mean: answered ? sum / answered : 0,
    worst,
    loss: sent ? (lost / sent) * 100 : 0,
  }
}
