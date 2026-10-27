import { onBeforeUnmount, onMounted, ref, shallowRef, watch, type Ref } from 'vue'

import { errorMessage } from '@/lib/async'
import { formatCount } from '@/lib/format'
import { matches } from '@/lib/search'
import { streamLost } from '@/lib/stream'

/** Whether a read was cancelled on purpose. */
const aborted = (e: unknown) =>
  typeof e === 'object' && e !== null && 'name' in e && e.name === 'AbortError'

/** Rows one read asks for. */
export const PAGE = 200
/** How long typing rests before the router is asked. */
export const SETTLE_MS = 250
/**
 * Rows the table keeps while Live adds to them. Past it the oldest go, and
 * scrolling reads them again.
 */
export const MAX_ROWS = 1000
/** How often Live adds what arrived, at most. */
export const LAND_MS = 250

/** One entry of a log the router keeps; seq numbers them in arrival order. */
export interface LogRow {
  seq?: number
}

/** One page of a log; a log's own page type may add fields. */
export interface LogPage<Row extends LogRow = LogRow> {
  entries?: Row[] | null
  next?: number
  more?: boolean
  searchedTo?: string
  held?: number
  oldest?: string
}

/** When an entry was logged, short: 25 Sep, 14:02. */
export function shortTime(iso: string) {
  return new Date(iso).toLocaleString(undefined, {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/**
 * What a log holds, as its header says it: "48,213 entries back to 25 Sep,
 * 14:02." While the log is kept in files the page reads on past memory, so
 * the count says it is memory's.
 */
export function heldLine(held: number, oldest?: string, inFiles = false) {
  if (!held) return 'No entries.'
  const noun = held === 1 ? 'entry' : 'entries'
  const where = inFiles ? ' in memory' : ''
  return oldest
    ? `${formatCount(held)} ${noun}${where} back to ${shortTime(oldest)}.`
    : `${formatCount(held)} ${noun}${where}.`
}

/**
 * Where a log behind a switch is kept and what clears it, as its card
 * says it. inFiles is whether the draft writes logs to files.
 */
export function keptLine(inFiles: boolean) {
  return inFiles
    ? 'Kept on this router only. Switching it off clears it, files included.'
    : 'Kept in memory on this router only. Switching it off, or a restart, clears it.'
}

export interface UseLogOptions<Row extends LogRow, Page extends LogPage<Row> = LogPage<Row>> {
  /** One page of the log. */
  read: (params: Record<string, unknown>, signal: AbortSignal) => Promise<Page>
  /** The log's event stream. */
  stream: string
  /** The page's selects, as query parameters. */
  filters?: () => Record<string, unknown>
  /** Whether a streamed row is in the selects' view, as the router would say. */
  keep?: (row: Row) => boolean
  /** What a row shows. */
  values: (row: Row) => unknown[]
  /** Brought into view when Live goes on. */
  top?: Ref<Element | { $el: Element } | null | undefined>
}

/**
 * The one way a page reads a log the router keeps: the newest page on
 * opening, the next as the table scrolls, the search asked of the router
 * once typing rests, and new rows over the stream while Live is on, landing
 * together at most every LAND_MS. Scrolling past the first page turns Live
 * off; what arrives meanwhile waits, and lands when Live is on again. A
 * hidden tab closes the stream.
 */
export function useLog<Row extends LogRow, Page extends LogPage<Row> = LogPage<Row>>({
  read,
  stream,
  filters = () => ({}),
  keep = () => true,
  values,
  top,
}: UseLogOptions<Row, Page>) {
  // Entries never change, so they are not made reactive one by one.
  const rows = shallowRef<Row[]>([])
  const query = ref('')
  const live = ref(true)
  const more = ref(false)
  const searchedTo = ref('')
  const held = ref(0)
  const oldest = ref('')
  /** The last first page read, for what a log says beside its rows. */
  const page = ref(null) as Ref<Page | null>
  const reading = ref(false)
  const readingMore = ref(false)
  const error = ref('')
  const streamError = ref('')
  const updatedAt = ref(0)

  let next = 0
  /** Arrivals not in the table yet, oldest first. */
  let waiting: Row[] = []
  /**
   * Arrivals were missed: the stream was closed, or more came than wait.
   * The newest page is read again before any land.
   */
  let missed = false
  /** The newest entry seen, so none lands twice. */
  let newest = 0
  let first: AbortController | null = null
  let later: AbortController | null = null
  let source: EventSource | null = null
  let settle = 0
  let landing = 0
  let landed = -Infinity

  function params(before: number) {
    const p: Record<string, unknown> = { ...filters(), limit: PAGE }
    const q = query.value.trim()
    if (q) p.q = q
    if (before) p.before = before
    return p
  }

  function take(p: Page) {
    more.value = Boolean(p.more)
    next = p.next ?? 0
    searchedTo.value = p.searchedTo ?? ''
    held.value = p.held ?? 0
    oldest.value = p.oldest ?? ''
  }

  /** Reads the newest page again, dropping any read still on its way. */
  async function reload() {
    first?.abort()
    later?.abort()
    const c = new AbortController()
    first = c
    reading.value = true
    missed = false
    try {
      const p = await read(params(0), c.signal)
      page.value = p
      rows.value = p.entries ?? []
      take(p)
      const topSeq = rows.value[0]?.seq ?? 0
      newest = Math.max(newest, topSeq)
      waiting = waiting.filter((e) => (e.seq ?? 0) > topSeq)
      error.value = ''
      updatedAt.value = Date.now()
      if (live.value) release()
    } catch (e) {
      if (!aborted(e)) {
        error.value = errorMessage(e)
        missed = true
      }
    } finally {
      if (first === c) {
        first = null
        reading.value = false
      }
    }
  }

  /** Reads the page after the rows the table holds. */
  async function loadMore() {
    if (!more.value || readingMore.value || reading.value) return
    // Scrolling past a full first page, as opposed to a search that
    // stopped on its budget carrying on.
    if (rows.value.length >= PAGE) live.value = false
    const c = new AbortController()
    later = c
    readingMore.value = true
    try {
      const p = await read(params(next), c.signal)
      rows.value = [...rows.value, ...(p.entries ?? [])]
      take(p)
      error.value = ''
    } catch (e) {
      if (!aborted(e)) error.value = errorMessage(e)
    } finally {
      if (later === c) {
        later = null
        readingMore.value = false
      }
    }
  }

  /** Puts streamed rows, newest first, on top: those the view and the search keep. */
  function add(list: Row[]) {
    const shown = list.filter((e) => keep(e) && matches(query.value, values(e)))
    if (!shown.length) return
    const all = shown.concat(rows.value)
    if (all.length > MAX_ROWS) {
      all.length = MAX_ROWS
      next = all[MAX_ROWS - 1]?.seq ?? 0
      more.value = true
    }
    rows.value = all
  }

  function release() {
    if (missed) {
      void reload()
      return
    }
    const list = waiting.reverse()
    waiting = []
    add(list)
  }

  function land() {
    landing = 0
    landed = performance.now()
    if (live.value && !reading.value) release()
  }

  /** At once after a quiet spell, then at most every LAND_MS. */
  function schedule() {
    if (landing) return
    landing = window.setTimeout(land, Math.max(0, landed + LAND_MS - performance.now()))
  }

  function arrive(e: Row) {
    if (!((e.seq ?? 0) > newest)) return
    newest = e.seq ?? 0
    waiting.push(e)
    if (waiting.length > MAX_ROWS) {
      waiting = []
      missed = true
    }
    if (live.value) schedule()
  }

  /** resumed: the tab shows again after the stream was closed. */
  function connect(resumed = false) {
    // EventSource sends the session cookie on same-origin requests.
    const es = new EventSource(stream)
    source = es
    let opened = resumed
    es.onopen = () => {
      streamError.value = ''
      // Reopened: a restarted router numbers its entries from 1 again, and
      // what was sent meanwhile never arrives.
      if (opened) {
        newest = 0
        waiting = []
        if (live.value) void reload()
        else missed = true
      }
      opened = true
    }
    es.onmessage = (m) => {
      try {
        arrive(JSON.parse(m.data as string) as Row)
      } catch {
        /* ignore malformed */
      }
    }
    es.onerror = () => {
      streamError.value = streamLost(es)
    }
  }

  function onVisibility() {
    if (document.hidden) {
      source?.close()
      source = null
    } else if (!source) connect(true)
  }

  /** Empties the table; what arrives from now on lands in it. */
  function clear() {
    rows.value = []
    waiting = []
    missed = false
    more.value = false
    searchedTo.value = ''
  }

  watch(live, (on) => {
    if (!on) return
    release()
    const target = top?.value
    const el = target instanceof Element ? target : target?.$el
    if (el && el.getBoundingClientRect().top < 0) el.scrollIntoView({ block: 'start' })
  })
  watch(query, () => {
    window.clearTimeout(settle)
    settle = window.setTimeout(() => void reload(), SETTLE_MS)
  })
  watch(filters, () => void reload())

  onMounted(() => {
    document.addEventListener('visibilitychange', onVisibility)
    if (!document.hidden) connect()
    void reload()
  })
  onBeforeUnmount(() => {
    document.removeEventListener('visibilitychange', onVisibility)
    source?.close()
    first?.abort()
    later?.abort()
    window.clearTimeout(settle)
    window.clearTimeout(landing)
  })

  return {
    rows,
    query,
    live,
    more,
    searchedTo,
    held,
    oldest,
    page,
    reading,
    readingMore,
    error,
    streamError,
    updatedAt,
    reload,
    loadMore,
    clear,
  }
}
