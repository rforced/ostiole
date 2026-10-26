/** One event of the wireless log; network is the one the interface serves. */
export interface WirelessEvent {
  seq: number
  time: string
  event: string
  interface: string
  mac: string
  network?: string
  device?: string
}

/**
 * The values a wireless log row shows, as it shows them: what the router
 * searches too (wirelesslog.Event.Search).
 */
export function wirelessValues(e: WirelessEvent): (string | undefined)[] {
  return [e.event, e.network, e.interface, e.device, e.mac]
}
