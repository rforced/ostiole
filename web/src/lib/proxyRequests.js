/**
 * The values a request's row shows, as it shows them: what the router
 * searches too (requestlog.Request.Search).
 * @param {object} r one request
 * @returns {Array<string | number | undefined>}
 */
export function requestValues(r) {
  return [r.site, r.client, r.method, r.host, r.path, r.status, r.agent]
}

/**
 * How long a request took, as the tab writes it: milliseconds under a
 * second, seconds past it.
 * @param {number} seconds
 */
export function tookText(seconds) {
  if (!(seconds >= 0)) return ''
  return seconds < 1 ? `${Math.round(seconds * 1000)} ms` : `${seconds.toFixed(1)} s`
}
