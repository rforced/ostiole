/**
 * The arithmetic behind a traffic chart: its windows, the round rates the
 * value axis stops at, the local times the time axis stops at, and which
 * point a pointer or a key is nearest. The drawing is TrafficChart.vue.
 */

/** A sample: time in seconds, down and up rates. */
export type Point = [number, number, number]

/** The windows a traffic chart spans, shortest first, in seconds. */
export const SPANS: Record<string, number> = { '5m': 300, '24h': 86400, '30d': 30 * 86400 }

/**
 * The top of the value axis and its step: a round step (1, 2, 2.5 or 5
 * of a power of ten) that divides the highest value into about four.
 * Nothing moving still gets an axis, of least: a kilobit a second unless
 * said.
 */
export function niceScale(highest: number, least = 1000): { top: number; step: number } {
  const high = Math.max(highest, least)
  const rough = high / 4
  const power = 10 ** Math.floor(Math.log10(rough))
  const step = [1, 2, 2.5, 5, 10].map((f) => f * power).find((s) => s >= rough) ?? 10 * power
  return { top: Math.ceil(high / step) * step, step }
}

/**
 * The value axis's stops, from zero to the top.
 */
export function rateTicks({ top, step }: { top: number; step: number }) {
  const out: number[] = []
  for (let v = 0; v <= top + step / 1e6; v += step) out.push(v)
  return out
}

/**
 * The time axis's stops between start and end, in seconds, at the local
 * times a reader rounds to: minutes over five minutes, hours over a day,
 * midnights over a month. A narrow chart takes every other one.
 * @param start seconds
 * @param end seconds
 * @param window a key of SPANS
 */
export function timeTicks(start: number, end: number, window: string, narrow = false) {
  const d = new Date(start * 1000)
  const out: number[] = []
  if (window === '30d') {
    const every = narrow ? 10 : 5
    d.setHours(0, 0, 0, 0)
    if (d.getTime() < start * 1000) d.setDate(d.getDate() + 1)
    for (; d.getTime() <= end * 1000; d.setDate(d.getDate() + every)) out.push(d.getTime() / 1000)
    return out
  }
  if (window === '24h') {
    const every = narrow ? 6 : 4
    d.setMinutes(0, 0, 0)
    while (d.getTime() < start * 1000 || d.getHours() % every !== 0) d.setHours(d.getHours() + 1)
    for (; d.getTime() <= end * 1000; d.setHours(d.getHours() + every)) out.push(d.getTime() / 1000)
    return out
  }
  const every = narrow ? 2 : 1
  d.setSeconds(0, 0)
  while (d.getTime() < start * 1000 || d.getMinutes() % every !== 0)
    d.setMinutes(d.getMinutes() + 1)
  for (; d.getTime() <= end * 1000; d.setMinutes(d.getMinutes() + every))
    out.push(d.getTime() / 1000)
  return out
}

/** A text-xs character at its widest, in pixels, for labels not yet drawn. */
const LABEL_CHAR_PX = 7

/**
 * The stops whose label, centred on the stop, fits between 0 and width.
 * A stop near either end would draw half its label outside the chart.
 * @param ticks seconds
 * @param xOf where a stop is, in pixels
 * @param labelOf what a stop says
 * @param width the chart's width, in pixels
 */
export function fittingTicks(
  ticks: number[],
  xOf: (t: number) => number,
  labelOf: (t: number) => string,
  width: number,
) {
  return ticks.filter((t) => {
    const half = (labelOf(t).length * LABEL_CHAR_PX) / 2
    return xOf(t) - half >= 0 && xOf(t) + half <= width
  })
}

/**
 * What a stop on the time axis says: 14:05 within a day, 27 Sep over a
 * month.
 * @param t seconds
 */
export function timeLabel(t: number, window: string) {
  const d = new Date(t * 1000)
  if (window === '30d') return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

/**
 * When a point was, as its readout says it: to the second over five
 * minutes, the minute over a day, the hour over a month.
 * @param t seconds
 */
export function pointLabel(t: number, window: string) {
  const d = new Date(t * 1000)
  if (window === '5m') return d.toLocaleTimeString()
  if (window === '24h')
    return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  return d.toLocaleString(undefined, {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/**
 * The index of the point nearest a time, or -1 when there are none.
 * Points run oldest first.
 * @param t seconds
 */
export function nearest(points: ArrayLike<number | null | undefined>[], t: number) {
  if (!points.length) return -1
  let lo = 0
  let hi = points.length - 1
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if ((points[mid][0] ?? 0) < t) lo = mid + 1
    else hi = mid
  }
  if (lo > 0 && t - (points[lo - 1][0] ?? 0) < (points[lo][0] ?? 0) - t) return lo - 1
  return lo
}

/**
 * An SVG path through one value of every point.
 * @param i 1 for down, 2 for up
 */
export function linePath(
  points: Point[],
  i: number,
  x: (t: number) => number,
  y: (v: number) => number,
) {
  let d = ''
  for (const p of points) d += `${d ? 'L' : 'M'}${x(p[0]).toFixed(1)},${y(p[i]).toFixed(1)}`
  return d
}

/**
 * An SVG path through one value of every point that has it, broken where a
 * value is null or the points are more than gap seconds apart. A point
 * without the value at all (undefined) is passed over.
 * @param i the value's index in a point
 * @param gap seconds, 0 never breaks
 */
export function seriesPath(
  points: ArrayLike<number | null | undefined>[],
  i: number,
  x: (t: number) => number,
  y: (v: number) => number,
  gap = 0,
) {
  let d = ''
  let prev: ArrayLike<number | null | undefined> | null = null
  for (const p of points) {
    const v = p[i]
    if (v === undefined) continue
    if (v === null) {
      prev = null
      continue
    }
    const t = p[0] ?? 0
    const move = !prev || (gap > 0 && t - (prev[0] ?? 0) > gap)
    d += `${move ? 'M' : 'L'}${x(t).toFixed(1)},${y(v).toFixed(1)}`
    prev = p
  }
  return d
}

/**
 * An SVG area between two values of every point, in runs broken as
 * seriesPath breaks.
 * @param lo the low value's index
 * @param hi the high value's index
 */
export function bandPath(
  points: ArrayLike<number | null | undefined>[],
  lo: number,
  hi: number,
  x: (t: number) => number,
  y: (v: number) => number,
  gap = 0,
) {
  const runs: Array<Array<[number, number, number]>> = []
  let run: Array<[number, number, number]> | null = null
  for (const p of points) {
    const low = p[lo]
    const high = p[hi]
    if (low === undefined) continue
    if (low === null || high == null) {
      run = null
      continue
    }
    const t = p[0] ?? 0
    if (!run || (gap > 0 && t - run[run.length - 1][0] > gap)) {
      run = []
      runs.push(run)
    }
    run.push([t, low, high])
  }
  const at = (t: number, v: number) => `${x(t).toFixed(1)},${y(v).toFixed(1)}`
  return runs
    .map((r) => {
      const top = r.map(([t, , high], k) => `${k ? 'L' : 'M'}${at(t, high)}`).join('')
      const bottom = r
        .slice()
        .reverse()
        .map(([t, low]) => `L${at(t, low)}`)
        .join('')
      return `${top}${bottom}Z`
    })
    .join('')
}
