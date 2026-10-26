import type { DhcpRow } from '@/lib/types.gen'

/** One message of the DHCP log; device is the name the router knows the client by. */
export type DhcpEvent = DhcpRow

/**
 * The values a DHCP log row shows, as it shows them: what the router
 * searches too (dhcplog.Event.Search).
 */
export function dhcpValues(e: DhcpEvent): (string | undefined)[] {
  return [e.message, e.interface, e.address, e.device, e.mac, e.duid, e.name, e.detail]
}
