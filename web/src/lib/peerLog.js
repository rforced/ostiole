/**
 * The values a VPN peer log row shows, as it shows them: what the router
 * searches too (peerlog.Event.Search).
 * @param {object} e one event
 * @returns {Array<string | undefined>}
 */
export function peerValues(e) {
  return [e.event, e.tunnel, e.peer, e.endpoint]
}
