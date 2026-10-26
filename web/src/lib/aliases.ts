import type { Alias } from '@/lib/types.gen'
import { isURL } from '@/lib/lists'

/** The URL lines of an alias's entries, each a list the router fetches. */
export function urlsOf(entries: string[] | null | undefined) {
  return (entries ?? []).filter(isURL)
}

/** Whether an alias of this type may hold URL lines. */
export function takesURLs(type: Alias['type'] | undefined) {
  return type === 'hosts' || type === 'ports'
}

/** Whether the router fetches this alias: a URL line in a hosts or ports alias, or a country or AS list. */
export function fetches(alias: Pick<Alias, 'type' | 'entries'>) {
  if (alias.type === 'geoip' || alias.type === 'asn') return true
  return takesURLs(alias.type) && urlsOf(alias.entries).length > 0
}
