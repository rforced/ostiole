import type { Config, Interface } from '@/lib/types.gen'

/**
 * What an interface is called wherever one is picked: the description when
 * the operator gave it one, with the kernel name after it so the two are
 * never confused. Without a description the kernel name is all there is.
 */
export function interfaceLabel(i?: Pick<Interface, 'name' | 'description'> | null): string {
  if (!i) return ''
  return i.description ? `${i.description} (${i.name})` : i.name
}

/**
 * Where a LAN-side service answers when no interface is picked: every
 * enabled interface in a zone that is not external. Mirrors the server's
 * own reading of an empty list.
 */
export function internalInterfaces(draft?: Config | null): Interface[] {
  if (!draft) return []
  const external = new Set((draft.zones ?? []).filter((z) => z.external).map((z) => z.name))
  return (draft.interfaces ?? []).filter((i) => i.enabled && i.zone && !external.has(i.zone))
}

/** The interfaces the DNS server listens on when none are picked. */
export const dnsListenInterfaces = internalInterfaces

/**
 * The interfaces a link-layer packet can go out on, read the way the
 * server's CheckWake reads a configuration: in a zone that is not
 * external, with Ethernet under them, and not a port of a bridge, a bond
 * or a PPPoE session. Wake on LAN and the discovery relay both use it.
 * Interfaces that are off are included; the caller decides about those.
 * cfg is a configuration, draft or saved.
 */
export function segmentInterfaces(cfg?: Config | null): Interface[] {
  if (!cfg) return []
  const zones = new Set((cfg.zones ?? []).filter((z) => !z.external).map((z) => z.name))
  const ports = new Set<string>()
  for (const i of cfg.interfaces ?? []) {
    for (const m of (i.bridge ?? i.bond)?.members ?? []) ports.add(m)
    if (i.pppoe?.parent) ports.add(i.pppoe.parent)
  }
  return (cfg.interfaces ?? []).filter(
    (i) =>
      !i.wireguard &&
      !i.openvpn &&
      !i.tailscale &&
      !i.pppoe &&
      !ports.has(i.name) &&
      i.zone !== undefined &&
      zones.has(i.zone),
  )
}

/**
 * Whether a link is read from the internet's side, as the server's
 * FacesInternet reads it: its zone is external, or it has no zone, listed
 * or not, and something carried over it does. Shaping takes download from
 * this.
 * cfg is a configuration, draft or saved.
 */
export function facesInternet(cfg: Config | null | undefined, name: string): boolean {
  const all = cfg?.interfaces ?? []
  const ports = (o: Interface) => [
    ...((o.bridge ?? o.bond)?.members ?? []),
    ...(o.pppoe?.parent ? [o.pppoe.parent] : []),
    ...(o.vlan?.parent ? [o.vlan.parent] : []),
  ]
  const seen = new Set<string>()
  const zones = cfg?.zones ?? []
  const faces = (n: string): boolean => {
    if (seen.has(n)) return false
    seen.add(n)
    const i = all.find((x) => x.name === n)
    if (i?.zone) return Boolean(zones.find((z) => z.name === i.zone)?.external)
    return all.some((o) => ports(o).includes(n) && faces(o.name))
  }
  return faces(name)
}
