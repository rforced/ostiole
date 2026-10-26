import type { QueryRow } from '@/lib/types.gen'

export type { QueryRow }

/**
 * The values a query's row shows, as it shows them: what the router
 * searches too (dnslog.Entry.Search).
 */
export function queryValues(e: QueryRow): (string | undefined)[] {
  return [e.device, e.client, e.name, e.type, e.status, ...(e.lists ?? []), e.answer]
}
