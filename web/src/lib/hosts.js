/** The key the diff uses for an override: the label, with its domain when it has one. */
export function overrideKey(h) {
  return h.domain ? `${h.hostname}.${h.domain}` : h.hostname
}

/** The name in full, under the local domain when the override has none. */
export function overrideName(h, local) {
  const domain = h.domain || local
  return domain ? `${h.hostname}.${domain}` : h.hostname
}
