/** Formatting helpers for the numbers the dashboard shows. */

const UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB']

/**
 * Human-readable byte count, e.g. 1536 -> "1.5 kB". Decimal units, because
 * that is what network gear quotes.
 *
 * @param {number} n
 * @returns {string}
 */
export function formatBytes(n) {
  const value = Number(n)
  if (!Number.isFinite(value) || value < 0) return '—'
  let i = 0
  let v = value
  while (v >= 1000 && i < UNITS.length - 1) {
    v /= 1000
    i += 1
  }
  const digits = i === 0 || v >= 100 ? 0 : 1
  return `${v.toFixed(digits)} ${UNITS[i]}`
}

/**
 * Packet and lease counts with thousands separators.
 *
 * @param {number} n
 * @returns {string}
 */
export function formatCount(n) {
  const value = Number(n)
  if (!Number.isFinite(value)) return '—'
  return value.toLocaleString()
}
