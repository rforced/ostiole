// The server's patterns for what a thing may be called (internal/model),
// so a dialog refuses on save what apply would.
const KINDS = {
  name: {
    re: /^[a-z][a-z0-9_]{0,30}$/,
    problem:
      'Name must be at most 31 lowercase letters, digits, or underscores and start with a letter.',
  },
  id: {
    re: /^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$/,
    problem:
      "Name must be at most 40 letters, digits, '_', '.', or '-' and start with a letter or digit.",
  },
  certificate: {
    re: /^[a-z0-9][a-z0-9-]{0,31}$/,
    problem:
      'Name must be at most 32 lowercase letters, digits, or hyphens and start with a letter or digit.',
  },
}

/**
 * Why the server would refuse name, or '' when it would not. 'name' is a
 * zone's, alias's, schedule's, gateway's, list's or peer's; 'id' an ACME
 * account's, DNS provider's, or the proxy's pools', sites' and profiles'.
 */
export function nameProblem(kind: keyof typeof KINDS, name: string): string {
  const k = KINDS[kind]
  return k.re.test(name) ? '' : k.problem
}

type Named = { id: string }
type ProxyLists = { pools?: Named[]; sites?: Named[]; wafProfiles?: Named[] }

/**
 * nameProblem for one of the proxy's pools, sites or profiles, which share
 * one set of names. previous is the item's name before this edit.
 */
export function proxyNameProblem(proxy: ProxyLists, id: string, previous = ''): string {
  const bad = nameProblem('id', id)
  if (bad || id === previous) return bad
  const lists: [string, Named[] | undefined][] = [
    ['Pool', proxy.pools],
    ['Site', proxy.sites],
    ['Profile', proxy.wafProfiles],
  ]
  const holder = lists.find(([, list]) => list?.some((x) => x.id === id))?.[0]
  return holder ? `${holder} ${id} already exists.` : ''
}
