/**
 * AS numbers as an alias writes them: "AS15169". The router reads a bare
 * number too, but one spelling keeps a list readable and its repeats
 * obvious. There is no picker: there are a hundred thousand of these.
 */

import { parseList } from '@/lib/lists'

/** The largest four-byte AS number. */
const MAX_ASN = 4294967295

/**
 * Reads one AS number written as "AS15169", "as15169" or "15169".
 */
export function parseAsn(text: string): number | null {
  const m = /^(?:as)?(\d{1,10})$/i.exec(String(text ?? '').trim())
  if (!m) return null
  const n = Number(m[1])
  return n >= 1 && n <= MAX_ASN ? n : null
}

/**
 * The canonical spelling.
 */
export function formatAsn(n: number): string {
  return `AS${n}`
}

/**
 * The canonical spelling of whatever was written, or the text as it was
 * when it is not an AS number, so it still lines up with what it names.
 */
export function canonicalAsn(text: string): string {
  const n = parseAsn(text)
  return n === null ? text : formatAsn(n)
}

/**
 * Splits a free-text list into canonical AS numbers, dropping repeats,
 * and says which items were not AS numbers at all.
 */
export function parseAsnList(text: string): { asns: string[]; invalid: string[] } {
  const asns = []
  const invalid = []
  const seen = new Set()
  for (const item of parseList(text)) {
    const n = parseAsn(item)
    if (n === null) {
      invalid.push(item)
      continue
    }
    if (seen.has(n)) continue
    seen.add(n)
    asns.push(formatAsn(n))
  }
  return { asns, invalid }
}
