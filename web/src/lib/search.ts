import { computed, ref } from 'vue'

/** The ways a MAC, or the start of one, is written in the wild. */
const MAC_WORDS = [
  /^[0-9a-f]{2}([:-][0-9a-f]{2})*[:-][0-9a-f]{1,2}$/, // aa-bb-cc, as Windows writes it
  /^[0-9a-f]{4}(\.[0-9a-f]{1,4})+$/, // aabb.ccdd, as a switch writes it
  /^[0-9a-f]{6,12}$/, // aabbcc
]

/**
 * A query word written as part of a MAC, with its separators taken out,
 * or '' for any other word. An address, a port or a short hex word such
 * as 1000 is not one.
 */
function macWord(word: string) {
  return MAC_WORDS.some((re) => re.test(word)) ? word.replace(/[:.-]/g, '') : ''
}

/**
 * Whether a row holds every word of a query, each somewhere in its values,
 * case aside. A MAC is also matched with its separators ignored, so
 * aa-bb-cc and aabb.cc find aa:bb:cc:dd:ee:ff.
 * @param macs values that are MACs
 */
export function matches(
  query: string,
  values: unknown[],
  macs: Array<string | null | undefined> = [],
) {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (!words.length) return true
  const text = [...values, ...macs]
    .filter((v) => v !== null && v !== undefined && v !== '')
    .map((v) => String(v).toLowerCase())
  const bare = macs
    .filter((m): m is string => Boolean(m))
    .map((m) => m.toLowerCase().replace(/[:.-]/g, ''))
  return words.every((w) => {
    if (text.some((v) => v.includes(w))) return true
    const hex = macWord(w)
    return Boolean(hex) && bare.some((m) => m.includes(hex))
  })
}

/**
 * The one search over a list the page holds.
 * @param fields what a row is searched by
 */
export function useSearch<T>(
  rows: import('vue').Ref<T[]>,
  fields: (row: T) => { values: unknown[]; macs?: string[] },
) {
  const query = ref('')
  const shown = computed(() => {
    if (!query.value.trim()) return rows.value
    return rows.value.filter((r) => {
      const { values, macs } = fields(r)
      return matches(query.value, values, macs)
    })
  })
  return { query, shown }
}
