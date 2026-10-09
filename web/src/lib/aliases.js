import { isURL } from '@/lib/lists'

/** The URL lines of an alias's entries, each a list the router fetches. */
export function urlsOf(entries) {
  return (entries ?? []).filter(isURL)
}

/** Whether an alias of this type may hold URL lines. */
export function takesURLs(type) {
  return type === 'hosts' || type === 'ports'
}

/** Whether the router fetches this alias: a URL line in a hosts or ports alias, or a country or AS list. */
export function fetches(alias) {
  if (alias.type === 'geoip' || alias.type === 'asn') return true
  return takesURLs(alias.type) && urlsOf(alias.entries).length > 0
}
