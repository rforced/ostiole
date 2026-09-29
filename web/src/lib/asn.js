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
 *
 * @param {string} text
 * @returns {number | null} the number, or null when it is not one
 */
export function parseAsn(text) {
  const m = /^(?:as)?(\d{1,10})$/i.exec(String(text ?? '').trim())
  if (!m) return null
  const n = Number(m[1])
  return n >= 1 && n <= MAX_ASN ? n : null
}

/**
 * The canonical spelling.
 *
 * @param {number} n
 * @returns {string}
 */
export function formatAsn(n) {
  return `AS${n}`
}

/**
 * The canonical spelling of whatever was written, or the text as it was
 * when it is not an AS number, so it still lines up with what it names.
 *
 * @param {string} text
 * @returns {string}
 */
export function canonicalAsn(text) {
  const n = parseAsn(text)
  return n === null ? text : formatAsn(n)
}

/**
 * Splits a free-text list into canonical AS numbers, dropping repeats,
 * and says which items were not AS numbers at all.
 *
 * @param {string} text
 * @returns {{ asns: string[], invalid: string[] }}
 */
export function parseAsnList(text) {
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
