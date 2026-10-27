import { fetches } from '@/lib/aliases'
import type { Alias } from '@/lib/types.gen'

/** The hosts aliases that are not fetched. */
export function smallAliases(aliases: Alias[]) {
  return aliases.filter((a) => a.type === 'hosts' && !fetches(a))
}

/**
 * Splits Allow from into the prefixes typed and the small aliases named.
 * Any other name stays with the prefixes, where the check will say why it
 * does not belong.
 */
export function splitAllowFrom(from: string[] = [], aliases: Alias[] = []) {
  const small = new Set(smallAliases(aliases).map((a) => a.name))
  return {
    prefixes: from.filter((f) => !small.has(f)),
    aliases: from.filter((f) => small.has(f)),
  }
}
