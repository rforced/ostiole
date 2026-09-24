/**
 * What an interface is called wherever one is picked: the description when
 * the operator gave it one, with the kernel name after it so the two are
 * never confused. Without a description the kernel name is all there is.
 */
export function interfaceLabel(i) {
  if (!i) return ''
  return i.description ? `${i.description} (${i.name})` : i.name
}

/**
 * Where a LAN-side service answers when no interface is picked: every
 * enabled interface in a zone that is not external. Mirrors the server's
 * own reading of an empty list.
 */
export function internalInterfaces(draft) {
  if (!draft) return []
  const external = new Set((draft.zones ?? []).filter((z) => z.external).map((z) => z.name))
  return (draft.interfaces ?? []).filter((i) => i.enabled && i.zone && !external.has(i.zone))
}

/** The interfaces the DNS server listens on when none are picked. */
export const dnsListenInterfaces = internalInterfaces
