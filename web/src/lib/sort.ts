import { computed, ref, toValue, type MaybeRefOrGetter } from 'vue'

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/** The direction a column runs. */
export type SortDir = 'asc' | 'desc'

/** How a table reads one column: the value of a row, and how two values compare. */
export interface SortColumn<Row = never> {
  value: (row: Row) => unknown
  compare: (a: unknown, b: unknown) => number
  first?: SortDir
}

const plain = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0)

/** The eight groups of an IPv6 address as one hex string, or null. */
function v6Key(ip: string) {
  const halves = (ip.split('%')[0] ?? '').toLowerCase().split('::')
  if (halves.length > 2) return null
  const head = halves[0] ? halves[0].split(':') : []
  const tail = halves.length === 2 && halves[1] ? halves[1].split(':') : []
  // An IPv4 address at the end, as in ::ffff:192.0.2.1, is two groups.
  const last = halves.length === 2 ? tail : head
  const dotted = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(last.at(-1) ?? '')
  if (dotted) {
    const [a = 0, b = 0, c = 0, d = 0] = dotted.slice(1).map(Number)
    last.splice(-1, 1, ((a << 8) | b).toString(16), ((c << 8) | d).toString(16))
  }
  const missing = 8 - head.length - tail.length
  if (halves.length === 2 ? missing < 1 : missing !== 0) return null
  const groups = [...head, ...Array<string>(halves.length === 2 ? missing : 0).fill('0'), ...tail]
  if (!groups.every((g) => /^[0-9a-f]{1,4}$/.test(g))) return null
  return groups.map((g) => g.padStart(4, '0')).join('')
}

/**
 * An address as a key that sorts the way the numbers do, IPv4 before
 * IPv6. Anything that is not an address goes after both, as written.
 */
export function addressKey(ip: string | null | undefined) {
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
export const byText = <Row>(get: (row: Row) => string): SortColumn<Row> => ({
  value: get,
  compare: (a, b) => collator.compare(a as string, b as string),
})

/** A column of addresses, in numeric order. */
export const byAddress = <Row>(get: (row: Row) => string | null | undefined): SortColumn<Row> => ({
  value: (r) => addressKey(get(r)),
  compare: (a, b) => plain(a as string, b as string),
})

/** A column of numbers, largest first unless the column reads upwards. */
export const byNumber = <Row>(
  get: (row: Row) => number | null | undefined,
  first: SortDir = 'desc',
): SortColumn<Row> => ({
  value: get,
  compare: (a, b) => (a as number) - (b as number),
  first,
})

/** A column of times, newest first unless the column reads forwards. */
export const byTime = <Row>(
  get: (row: Row) => string | number | null | undefined,
  first: SortDir = 'desc',
): SortColumn<Row> => ({
  value: (r) => {
    const t = get(r)
    const ms = t ? new Date(t).getTime() : NaN
    return Number.isFinite(ms) ? ms : null
  },
  compare: (a, b) => (a as number) - (b as number),
  first,
})

const empty = (v: unknown) =>
  v === null || v === undefined || v === '' || (typeof v === 'number' && Number.isNaN(v))

/** Empty values last whichever way the column runs. */
function compareEmptyLast(
  a: unknown,
  b: unknown,
  compare: (a: unknown, b: unknown) => number,
  sign: number,
) {
  const ae = empty(a)
  const be = empty(b)
  if (ae || be) return ae === be ? 0 : ae ? 1 : -1
  return sign * compare(a, b)
}

/**
 * The order of a sortable table. A column's first click sorts it the way
 * it reads, the second reverses it. A header over several columns, such
 * as a cell's rate and its total, moves to the next one instead. Empty
 * values go last either way, and ties fall to the tie column, then to the
 * order the rows came in. With no column to start from, a list keeps the
 * order it has until a header is clicked.
 */
export function useSort<Row>(
  rows: MaybeRefOrGetter<Row[]>,
  columns: Record<string, SortColumn<Row>>,
  { by = null, dir, tie }: { by?: string | null; dir?: SortDir; tie?: string } = {},
) {
  function column(key: string) {
    const col = columns[key]
    if (!col) throw new TypeError(`no sort column ${key}`)
    return col
  }

  const chosen = ref<{ by: string | null; dir: SortDir | null }>(
    by ? { by, dir: dir ?? column(by).first ?? 'asc' } : { by: null, dir: null },
  )
  const order = computed(() => chosen.value)

  /**
   * A header click: this column in its own order, or the other way. Over
   * several columns, the next of them in its own order.
   */
  function toggle(key: string | string[]) {
    const keys = [key].flat()
    const { by: current, dir: d } = chosen.value
    if (keys.length === 1 && current === keys[0])
      chosen.value = { by: current, dir: d === 'asc' ? 'desc' : 'asc' }
    else {
      const next = keys[(keys.indexOf(current ?? '') + 1) % keys.length]
      if (next !== undefined) choose(next)
    }
  }

  /** The phone's select: a column in its own order. */
  function choose(key: string) {
    if (!columns[key]) return
    chosen.value = { by: key, dir: columns[key].first ?? 'asc' }
  }

  const sorted = computed(() => {
    const { by: key, dir: d } = order.value
    if (!key) return toValue(rows)
    const col = column(key)
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

  return { sorted, order, toggle, choose }
}
