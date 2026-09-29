/**
 * The values a wireless log row shows, as it shows them: what the router
 * searches too (wirelesslog.Event.Search).
 * @param {object} e one event
 * @returns {Array<string | undefined>}
 */
export function wirelessValues(e) {
  return [e.event, e.network, e.interface, e.device, e.mac]
}
