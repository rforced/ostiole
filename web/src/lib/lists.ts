/** Whether a list item is an http or https URL. */
export function isURL(item: string | null | undefined) {
  return /^https?:\/\//i.test(String(item ?? '').trim())
}

/**
 * Splits a free-text list (newlines, commas, or spaces) into trimmed items.
 * A URL stays whole, since it may hold a comma.
 */
export function parseList(text: string | null | undefined) {
  return String(text ?? '')
    .split(/\s+/)
    .flatMap((item) => (isURL(item) ? [item] : item.split(',')))
    .map((s) => s.trim())
    .filter(Boolean)
}

/** One item per line, for textareas. */
export function joinList(items: string[] | null | undefined) {
  return (items ?? []).join('\n')
}

/** The saved list when it holds the same items, so an untouched save keeps its order. */
export function keptOrder(was: string[] | null | undefined, now: string[]) {
  const key = (list: string[] | null | undefined) => [...(list ?? [])].sort().join('\n')
  return was && key(was) === key(now) ? [...was] : now
}

/**
 * The first few items of a list and how many more, for a table cell that
 * cannot hold them all: "rule r1, rule r2 and 3 more".
 */
export function someOf(items: string[], shown = 2) {
  const head = items.slice(0, shown).join(', ')
  const rest = items.length - shown
  return rest > 0 ? `${head} and ${rest} more` : head
}
