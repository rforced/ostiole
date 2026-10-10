/** The block a router without one reads as, which the store never writes. */
export const DISCOVERY_DEFAULT = Object.freeze({ enabled: false, mdns: true, ssdp: true })

/**
 * Whether the relay runs on a configuration, as the server's
 * DiscoveryActive reads it: on, a protocol on, and among the enabled
 * interfaces in it one that asks and one that answers.
 * @param {object} [cfg]
 */
export function discoveryActive(cfg) {
  const d = cfg?.services?.discovery
  if (!d?.enabled || !(d.mdns || d.ssdp)) return false
  const on = new Set((cfg.interfaces ?? []).filter((i) => i.enabled).map((i) => i.name))
  const links = (d.interfaces ?? []).filter((l) => on.has(l.interface))
  return links.some((l) => l.asks) && links.some((l) => l.answers)
}

/**
 * The values a discovery log row shows, as it shows them: what the router
 * searches too.
 * @param {object} e one packet
 * @returns {Array<string | undefined>}
 */
export function discoveryValues(e) {
  return [e.protocol, e.kind, e.from, ...(e.to ?? []), e.name, e.source, e.dropped]
}
