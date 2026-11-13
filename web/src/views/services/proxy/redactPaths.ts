/** The most bytes a hidden path prefix may hold, as the router checks it. */
export const REDACT_PATH_MAX_LEN = 48

/** The most different prefixes the proxy hides across all its sites. */
export const REDACT_PATHS_MAX = 10

const UNRESERVED = /^[A-Za-z0-9._~/-]+$/

/**
 * What the router would refuse in a site's hidden path prefixes, in its
 * words, or '' when it takes them. `others` are the prefixes the proxy's
 * other sites hide, since the cap counts them all once.
 */
export function redactPathsProblem(prefixes: string[], others: string[] = []) {
  const seen = new Set<string>()
  for (const r of prefixes) {
    const q = JSON.stringify(r)
    if (r.length < 3 || !r.startsWith('/') || !r.endsWith('/')) {
      return `${q} must start and end with / and hold at least 3 characters`
    }
    if (new TextEncoder().encode(r).length > REDACT_PATH_MAX_LEN) {
      return `${q} is longer than ${REDACT_PATH_MAX_LEN} characters`
    }
    if (!UNRESERVED.test(r)) return `${q} may hold only letters, digits and - . _ ~ /`
    if (r.includes('//')) return `${q} must not hold //`
    if (seen.has(r)) return `duplicate prefix ${q}`
    seen.add(r)
  }
  if (new Set([...others, ...prefixes]).size > REDACT_PATHS_MAX) {
    return `the proxy hides at most ${REDACT_PATHS_MAX} different prefixes across its sites`
  }
  return ''
}
