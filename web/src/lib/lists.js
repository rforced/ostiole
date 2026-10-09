/** Whether a list item is an http or https URL. */
export function isURL(item) {
  return /^https?:\/\//i.test(String(item ?? '').trim())
}

/**
 * Splits a free-text list (newlines, commas, or spaces) into trimmed items.
 * A URL stays whole, since it may hold a comma.
 */
export function parseList(text) {
  return String(text ?? '')
    .split(/\s+/)
    .flatMap((item) => (isURL(item) ? [item] : item.split(',')))
    .map((s) => s.trim())
    .filter(Boolean)
}

/** One item per line, for textareas. */
export function joinList(items) {
  return (items ?? []).join('\n')
}

/**
 * The first few items of a list and how many more, for a table cell that
 * cannot hold them all: "rule r1, rule r2 and 3 more".
 *
 * @param {string[]} items
 * @param {number} [shown]
 */
export function someOf(items, shown = 2) {
  const head = items.slice(0, shown).join(', ')
  const rest = items.length - shown
  return rest > 0 ? `${head} and ${rest} more` : head
}
