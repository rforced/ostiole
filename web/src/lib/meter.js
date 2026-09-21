/**
 * The one colour map for a ratio against a limit.
 *
 * Severity colours the fill, and the track takes the same hue at a fifth of
 * its strength so the state reads across the whole bar rather than only the
 * filled part. The number beside a meter stays in ordinary ink: the colour
 * repeats what the number already says rather than being the only way to
 * read it, which is what keeps a meter legible to a colour-blind reader and
 * in print.
 */
export const LEVELS = {
  ok: { fill: 'bg-accent', track: 'bg-accent/20' },
  warn: { fill: 'bg-warn', track: 'bg-warn/20' },
  critical: { fill: 'bg-bad', track: 'bg-bad/20' },
}

/**
 * Where a meter stops being reassuring. A router at 75% memory is worth a
 * glance; at 90% of a disk it is worth acting on, and a connection table at
 * 100% refuses new connections outright.
 */
export const WARN = 75
export const CRITICAL = 90

/**
 * The severity of a percentage, for {@link LEVELS}. Nothing measured yet is
 * not a fault, so it reads as fine.
 *
 * @param {number | null | undefined} percent
 * @returns {'ok' | 'warn' | 'critical'}
 */
export function levelOf(percent) {
  if (percent === null || percent === undefined) return 'ok'
  if (percent >= CRITICAL) return 'critical'
  if (percent >= WARN) return 'warn'
  return 'ok'
}
