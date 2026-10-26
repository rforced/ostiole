/** One event of a VPN peer's log. */
export interface PeerEvent {
  seq: number
  time: string
  event: string
  tunnel?: string
  peer: string
  endpoint?: string
}

/**
 * The values a VPN peer log row shows, as it shows them: what the router
 * searches too (peerlog.Event.Search).
 */
export function peerValues(e: PeerEvent): (string | undefined)[] {
  return [e.event, e.tunnel, e.peer, e.endpoint]
}
