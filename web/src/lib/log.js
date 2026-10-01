import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { errorMessage } from '@/lib/async'
import { formatCount } from '@/lib/format'
import { matches } from '@/lib/search'
import { streamLost } from '@/lib/stream'

/** Rows one read asks for. */
export const PAGE = 200
/** How long typing rests before the router is asked. */
export const SETTLE_MS = 250
/**
 * Rows the table keeps while Live adds to them. Past it the oldest go, and
 * scrolling reads them again.
 */
export const MAX_ROWS = 10000

/**
 * When an entry was logged, short: 25 Sep, 14:02.
 * @param {string} iso
 */
export function shortTime(iso) {
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
 * @param {number} held
 * @param {string} [oldest]
 * @param {boolean} [inFiles]
 */
export function heldLine(held, oldest, inFiles = false) {
  if (!held) return 'No entries.'
  const noun = held === 1 ? 'entry' : 'entries'
  const where = inFiles ? ' in memory' : ''
  return oldest
    ? `${formatCount(held)} ${noun}${where} back to ${shortTime(oldest)}.`
    : `${formatCount(held)} ${noun}${where}.`
}

/**
 * Where a log behind a switch is kept and what clears it, as its card
 * says it.
 * @param {boolean} inFiles whether the draft writes logs to files
 */
export function keptLine(inFiles) {
  return inFiles
    ? 'Kept on this router only. Switching it off clears it, files included.'
    : 'Kept in memory on this router only. Switching it off, or a restart, clears it.'
}

/**
 * The one way a page reads a log the router keeps: the newest page on
 * opening, the next as the table scrolls, the search asked of the router
 * once typing rests, and new rows over the stream while Live is on.
 * Scrolling past the first page turns Live off; what arrives meanwhile
 * waits, and lands when Live is on again.
 *
 * @param {object} o
 * @param {(params: object, signal: AbortSignal) => Promise<object>} o.read
 *   one page: {entries, next, more, searchedTo, held, oldest}
 * @param {string} o.stream the log's event stream
 * @param {() => object} [o.filters] the page's selects, as query parameters
 * @param {(row: object) => boolean} [o.keep] whether a streamed row is in
 *   the selects' view, as the router would say
 * @param {(row: object) => any[]} o.values what a row shows
 * @param {import('vue').Ref<any>} [o.top] brought into view when Live goes on
 */
export function useLog({ read, stream, filters = () => ({}), keep = () => true, values, top }) {
  const rows = ref([])
  const query = ref('')
  const live = ref(true)
  const more = ref(false)
  const searchedTo = ref('')
  const held = ref(0)
  const oldest = ref('')
  /** The last first page read, for what a log says beside its rows. */
  const page = ref(null)
  const reading = ref(false)
  const readingMore = ref(false)
  const error = ref('')
  const streamError = ref('')
  const updatedAt = ref(0)

  let next = 0
  /** Arrivals while Live is off or a first page is read, newest first. */
  let waiting = []
  /** The newest entry seen, so none lands twice. */
  let newest = 0
  let first = null
  let later = null
  let source = null
  let settle = 0

  function params(before) {
    const p = { ...filters(), limit: PAGE }
    const q = query.value.trim()
    if (q) p.q = q
    if (before) p.before = before
    return p
  }

  function take(p) {
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
    try {
      const p = await read(params(0), c.signal)
      page.value = p
      rows.value = p.entries ?? []
      take(p)
      const topSeq = rows.value[0]?.seq ?? 0
      newest = Math.max(newest, topSeq)
      waiting = waiting.filter((e) => e.seq > topSeq)
      error.value = ''
      updatedAt.value = Date.now()
      if (live.value) release()
    } catch (e) {
      if (e?.name !== 'AbortError') error.value = errorMessage(e)
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
      if (e?.name !== 'AbortError') error.value = errorMessage(e)
    } finally {
      if (later === c) {
        later = null
        readingMore.value = false
      }
    }
  }

  /** Puts streamed rows on top: those the view and the search keep. */
  function add(list) {
    const shown = list.filter((e) => keep(e) && matches(query.value, values(e)))
    if (!shown.length) return
    rows.value = [...shown, ...rows.value]
    if (rows.value.length > MAX_ROWS) {
      rows.value = rows.value.slice(0, MAX_ROWS)
      next = rows.value[rows.value.length - 1].seq
      more.value = true
    }
  }

  function release() {
    const list = waiting
    waiting = []
    add(list)
  }

  function arrive(e) {
    if (!(e.seq > newest)) return
    newest = e.seq
    if (live.value && !reading.value) add([e])
    else waiting = [e, ...waiting].slice(0, MAX_ROWS)
  }

  function connect() {
    // EventSource sends the session cookie on same-origin requests.
    const es = new EventSource(stream)
    source = es
    let opened = false
    es.onopen = () => {
      streamError.value = ''
      // Reopened after a drop: a restarted router numbers its entries
      // from 1 again, and what was sent meanwhile never arrives.
      if (opened) {
        newest = 0
        waiting = []
        reload()
      }
      opened = true
    }
    es.onmessage = (m) => {
      try {
        arrive(JSON.parse(m.data))
      } catch {
        /* ignore malformed */
      }
    }
    es.onerror = () => {
      streamError.value = streamLost(es)
    }
  }

  /** Empties the table; what arrives from now on lands in it. */
  function clear() {
    rows.value = []
    waiting = []
    more.value = false
    searchedTo.value = ''
  }

  watch(live, (on) => {
    if (!on) return
    release()
    const el = top?.value?.$el ?? top?.value
    if (el?.getBoundingClientRect?.().top < 0) el.scrollIntoView?.({ block: 'start' })
  })
  watch(query, () => {
    window.clearTimeout(settle)
    settle = window.setTimeout(reload, SETTLE_MS)
  })
  watch(filters, reload)

  onMounted(() => {
    connect()
    reload()
  })
  onBeforeUnmount(() => {
    source?.close()
    first?.abort()
    later?.abort()
    window.clearTimeout(settle)
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
