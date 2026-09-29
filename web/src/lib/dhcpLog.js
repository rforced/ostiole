/**
 * The values a DHCP log row shows, as it shows them: what the router
 * searches too (dhcplog.Event.Search).
 * @param {object} e one message
 * @returns {Array<string | undefined>}
 */
export function dhcpValues(e) {
  return [e.message, e.interface, e.address, e.device, e.mac, e.duid, e.name, e.detail]
}
