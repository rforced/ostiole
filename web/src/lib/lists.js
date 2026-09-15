/** Splits a free-text list (newlines, commas, or spaces) into trimmed items. */
export function parseList(text) {
  return String(text ?? '')
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

/** One item per line, for textareas. */
export function joinList(items) {
  return (items ?? []).join('\n')
}
