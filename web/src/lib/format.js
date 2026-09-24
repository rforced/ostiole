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
 * A span of seconds as the two units that matter, e.g. 93784 -> "1d 2h".
 * Uptime is read at a glance; the seconds never are.
 *
 * @param {number} seconds
 * @returns {string}
 */
export function formatDuration(seconds) {
  const total = Math.floor(Number(seconds))
  if (!Number.isFinite(total) || total < 0) return '—'
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
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

/**
 * How far a clock is from true time, e.g. -0.00031 -> "0.31 ms behind".
 * Positive is ahead. Two significant figures: nobody reads a clock's
 * offset to the nanosecond.
 *
 * @param {number} seconds
 * @returns {string}
 */
export function formatOffset(seconds) {
  const v = Number(seconds)
  if (!Number.isFinite(v)) return '—'
  const abs = Math.abs(v)
  const way = v > 0 ? ' ahead' : ' behind'
  if (abs >= 1) return `${Number(abs.toPrecision(3))} s${way}`
  if (abs >= 0.001) return `${Number((abs * 1e3).toPrecision(2))} ms${way}`
  const us = Math.round(abs * 1e6)
  return us === 0 ? '0 µs' : `${us} µs${way}`
}

const RATE_UNITS = ['bit/s', 'kbit/s', 'Mbit/s', 'Gbit/s']

/**
 * A line speed the way a line is sold: 20000000 -> "20 Mbit/s". Decimal
 * again, and bits rather than bytes, because that is what an operator was
 * quoted and what they will type back in.
 *
 * @param {number} bits
 * @returns {string}
 */
export function formatRate(bits) {
  const value = Number(bits)
  if (!Number.isFinite(value) || value < 0) return '—'
  if (value === 0) return '0 bit/s'
  let i = 0
  let v = value
  while (v >= 1000 && i < RATE_UNITS.length - 1) {
    v /= 1000
    i += 1
  }
  // Three significant figures, and no trailing zeros: 2.5 Gbit/s is what
  // somebody was sold, 2.50 Gbit/s is a reading nobody takes.
  const digits = v >= 100 ? 0 : v >= 10 ? 1 : 2
  return `${Number(v.toFixed(digits))} ${RATE_UNITS[i]}`
}
