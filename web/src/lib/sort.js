import { computed, ref, toValue, watch } from 'vue'

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })
const plain = (a, b) => (a < b ? -1 : a > b ? 1 : 0)

/** The eight groups of an IPv6 address as one hex string, or null. */
function v6Key(ip) {
  const halves = ip.split('%')[0].toLowerCase().split('::')
  if (halves.length > 2) return null
  const head = halves[0] ? halves[0].split(':') : []
  const tail = halves.length === 2 && halves[1] ? halves[1].split(':') : []
  // An IPv4 address at the end, as in ::ffff:192.0.2.1, is two groups.
  const last = halves.length === 2 ? tail : head
  const dotted = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(last.at(-1) ?? '')
  if (dotted) {
    const [a, b, c, d] = dotted.slice(1).map(Number)
    last.splice(-1, 1, ((a << 8) | b).toString(16), ((c << 8) | d).toString(16))
  }
  const missing = 8 - head.length - tail.length
  if (halves.length === 2 ? missing < 1 : missing !== 0) return null
  const groups = [...head, ...Array(halves.length === 2 ? missing : 0).fill('0'), ...tail]
  if (!groups.every((g) => /^[0-9a-f]{1,4}$/.test(g))) return null
  return groups.map((g) => g.padStart(4, '0')).join('')
}

/**
 * An address as a key that sorts the way the numbers do, IPv4 before
 * IPv6. Anything that is not an address goes after both, as written.
 * @param {string | null | undefined} ip
 */
export function addressKey(ip) {
  if (!ip) return null
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(ip)
  if (v4)
    return `4${v4
      .slice(1)
      .map((o) => Number(o).toString(16).padStart(2, '0'))
      .join('')}`
  const v6 = ip.includes(':') ? v6Key(ip) : null
  return v6 ? `6${v6}` : `9${ip.toLowerCase()}`
}

/** A column of words, A to Z, with numbers in them read as numbers: host2 before host10. */
export const byText = (get) => ({ value: get, compare: (a, b) => collator.compare(a, b) })

/** A column of addresses, in numeric order. */
export const byAddress = (get) => ({ value: (r) => addressKey(get(r)), compare: plain })

/** A column of numbers, largest first unless the column reads upwards. */
export const byNumber = (get, first = 'desc') => ({
  value: get,
  compare: (a, b) => a - b,
  first,
})

/** A column of times, newest first unless the column reads forwards. */
export const byTime = (get, first = 'desc') => ({
  value: (r) => {
    const t = get(r)
    const ms = t ? new Date(t).getTime() : NaN
    return Number.isFinite(ms) ? ms : null
  },
  compare: (a, b) => a - b,
  first,
})

const empty = (v) => v === null || v === undefined || v === '' || Number.isNaN(v)

/** Empty values last whichever way the column runs. */
function compareEmptyLast(a, b, compare, sign) {
  const ae = empty(a)
  const be = empty(b)
  if (ae || be) return ae === be ? 0 : ae ? 1 : -1
  return sign * compare(a, b)
}

/**
 * The order of a sortable table. A column's first click sorts it the way
 * it reads, the second reverses it. Empty values go last either way, and
 * ties fall to the tie column, then to the order the rows came in. With
 * no column to start from, a list keeps the order it has until a header
 * is clicked. While lock returns an order, as Live holds a list newest
 * first, that order stands and a click does nothing; when it lets go, the
 * table keeps it.
 *
 * @param {import('vue').MaybeRefOrGetter<any[]>} rows
 * @param {Record<string, {value: (row: any) => any, compare: (a: any, b: any) => number, first?: 'asc' | 'desc'}>} columns
 * @param {{by?: string | null, dir?: 'asc' | 'desc', tie?: string, lock?: () => ({by: string, dir: 'asc' | 'desc'} | null)}} [options]
 */
export function useSort(rows, columns, { by = null, dir, tie, lock } = {}) {
  const chosen = ref(by ? { by, dir: dir ?? columns[by].first ?? 'asc' } : { by: null, dir: null })
  const held = computed(() => lock?.() ?? null)
  const order = computed(() => held.value ?? chosen.value)
  const locked = computed(() => Boolean(held.value))
  // Synchronous, so a lock taken and let go within one tick still leaves
  // its order behind.
  watch(
    held,
    (now, before) => {
      if (!now && before) chosen.value = { ...before }
    },
    { flush: 'sync' },
  )

  /** A header click: this column in its own order, or the other way. */
  function toggle(key) {
    if (locked.value) return
    const { by: current, dir: d } = chosen.value
    chosen.value =
      current === key
        ? { by: key, dir: d === 'asc' ? 'desc' : 'asc' }
        : { by: key, dir: columns[key].first ?? 'asc' }
  }

  /** The phone's select: a column in its own order. */
  function choose(key) {
    if (locked.value || !columns[key]) return
    chosen.value = { by: key, dir: columns[key].first ?? 'asc' }
  }

  const sorted = computed(() => {
    const { by: key, dir: d } = order.value
    if (!key) return toValue(rows)
    const col = columns[key]
    const tied = tie ? columns[tie] : null
    const sign = d === 'desc' ? -1 : 1
    return toValue(rows)
      .map((row, i) => ({ row, i, v: col.value(row), t: tied?.value(row) }))
      .sort(
        (a, b) =>
          compareEmptyLast(a.v, b.v, col.compare, sign) ||
          (tied ? compareEmptyLast(a.t, b.t, tied.compare, 1) : 0) ||
          a.i - b.i,
      )
      .map((x) => x.row)
  })

  return { sorted, order, locked, toggle, choose }
}
