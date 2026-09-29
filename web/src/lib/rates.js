/**
 * Turns the byte counters of successive interface samples into bit
 * rates. The kernel only counts bytes since a link came up, so a rate
 * needs two readings and the time between them.
 */

/** Two samples closer than this measure jitter, not traffic. */
const MIN_SECONDS = 1

/**
 * Returns a sampler. Call it with each new list of links; it answers
 * with bits per second for every link it has now seen twice, keeping
 * the last answer for a link when the new sample came too soon.
 *
 * @returns {(links: Array<{name: string, rxBytes: number, txBytes: number}>, now?: number) => Record<string, {rx: number, tx: number}>}
 */
export function createRateTracker() {
  /** @type {Map<string, {rx: number, tx: number, at: number, rate?: {rx: number, tx: number}}>} */
  let last = new Map()
  return function sample(links, now = Date.now()) {
    const rates = {}
    const next = new Map()
    for (const l of links) {
      const prev = last.get(l.name)
      const entry = { rx: l.rxBytes ?? 0, tx: l.txBytes ?? 0, at: now, rate: prev?.rate }
      if (prev) {
        const seconds = (now - prev.at) / 1000
        if (seconds < MIN_SECONDS) {
          // Too soon: keep the old baseline so the next sample has a window.
          entry.rx = prev.rx
          entry.tx = prev.tx
          entry.at = prev.at
        } else if (entry.rx >= prev.rx && entry.tx >= prev.tx) {
          entry.rate = {
            rx: ((entry.rx - prev.rx) * 8) / seconds,
            tx: ((entry.tx - prev.tx) * 8) / seconds,
          }
        } else {
          // A counter went backwards: the link was reset. Start over.
          entry.rate = undefined
        }
      }
      next.set(l.name, entry)
      if (entry.rate) rates[l.name] = entry.rate
    }
    last = next
    return rates
  }
}
