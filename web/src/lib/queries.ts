/** One answer of the query log. */
export interface QueryRow {
  seq: number
  time: string
  client: string
  device?: string
  name: string
  type: string
  status: string
  reason?: string
  lists?: string[]
  answer?: string
}

/**
 * The values a query's row shows, as it shows them: what the router
 * searches too (dnslog.Entry.Search).
 */
export function queryValues(e: QueryRow): (string | undefined)[] {
  return [e.device, e.client, e.name, e.type, e.status, ...(e.lists ?? []), e.answer]
}
