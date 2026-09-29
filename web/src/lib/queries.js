/**
 * The values a query's row shows, as it shows them: what the router
 * searches too (dnslog.Entry.Search).
 * @param {object} e one answer
 * @returns {Array<string | undefined>}
 */
export function queryValues(e) {
  return [e.device, e.client, e.name, e.type, e.status, ...(e.lists ?? []), e.answer]
}
