/**
 * Turns the byte counters of successive interface samples into bit
 * rates. The kernel gives totals, so a rate needs two readings and the
 * time between them.
 */

/** Two samples closer than this measure jitter, not traffic. */
const MIN_SECONDS = 1

type Rate = { rx: number; tx: number }
type Sample = Rate & { at: number; rate?: Rate }

/**
 * Returns a sampler. Call it with each new list of links; it answers
 * with bits per second for every link it has now seen twice, keeping
 * the last answer for a link when the new sample came too soon.
 */
export function createRateTracker(): (
  links: Array<{ name: string; rxBytes: number; txBytes: number }>,
  now?: number,
) => Record<string, Rate> {
  let last = new Map<string, Sample>()
  return function sample(links, now = Date.now()) {
    const rates: Record<string, Rate> = {}
    const next = new Map<string, Sample>()
    for (const l of links) {
      const prev = last.get(l.name)
      const entry: Sample = { rx: l.rxBytes ?? 0, tx: l.txBytes ?? 0, at: now, rate: prev?.rate }
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
