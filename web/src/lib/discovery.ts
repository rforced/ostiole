import type { Config, DiscoveryEvent } from '@/lib/types.gen'

export type { DiscoveryEvent }

/** The block a router without one reads as; a missing protocol is on, as the server reads it. */
export const DISCOVERY_DEFAULT = Object.freeze({ enabled: false, mdns: true, ssdp: true })

/**
 * Whether the relay runs on a configuration, as the server's
 * DiscoveryActive reads it: on, a protocol on, and among the enabled
 * interfaces in it one that asks and one that answers.
 */
export function discoveryActive(cfg?: Config | null): boolean {
  const d = cfg?.services?.discovery
  if (!d?.enabled || !((d.mdns ?? true) || (d.ssdp ?? true))) return false
  const on = new Set((cfg?.interfaces ?? []).filter((i) => i.enabled).map((i) => i.name))
  const links = (d.interfaces ?? []).filter((l) => on.has(l.interface))
  return links.some((l) => l.asks) && links.some((l) => l.answers)
}

/**
 * The values a discovery log row shows, as it shows them: what the router
 * searches too.
 */
export function discoveryValues(e: DiscoveryEvent): (string | undefined)[] {
  return [e.protocol, e.kind, e.from, ...(e.to ?? []), e.name, e.source, e.dropped]
}
