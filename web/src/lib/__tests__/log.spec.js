import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'

import { MAX_ROWS, PAGE, SETTLE_MS, heldLine, useLog } from '@/lib/log'

/** The stream the log opens; a test sends through the last one. */
let source = null
class FakeSource {
  static CLOSED = 2
  constructor(url) {
    this.url = url
    source = this
  }
  close() {
    this.closed = true
  }
}
const send = (e) => source.onmessage({ data: JSON.stringify(e) })

/** A log of n entries, newest first, served a page at a time like the router. */
function router(n) {
  const all = Array.from({ length: n }, (_, i) => ({ seq: n - i, word: `w${n - i}` }))
  return vi.fn(async (params) => {
    const from = params.before ? all.findIndex((e) => e.seq < params.before) : 0
    const found = all
      .slice(from)
      .filter((e) => !params.q || e.word.includes(params.q))
      .slice(0, params.limit)
    const last = found[found.length - 1]
    const more = Boolean(last) && last.seq > 1
    return { entries: found, more, next: more ? last.seq : undefined, held: n }
  })
}

function harness(read, extra = {}) {
  let log
  const Host = defineComponent({
    setup() {
      log = useLog({ read, stream: '/stream', values: (e) => [e.word], ...extra })
      return () => h('div')
    },
  })
  const wrapper = mount(Host)
  return { wrapper, log: () => log }
}

const seqs = (log) => log.rows.value.map((e) => e.seq)

describe('useLog', () => {
  beforeEach(() => {
    vi.stubGlobal('EventSource', FakeSource)
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('reads the newest page, and the next as asked', async () => {
    const read = router(450)
    const { log } = harness(read)
    await flushPromises()
    expect(read).toHaveBeenCalledWith({ limit: PAGE }, expect.any(AbortSignal))
    expect(seqs(log())).toHaveLength(200)
    expect(log().more.value).toBe(true)
    expect(log().held.value).toBe(450)

    await log().loadMore()
    expect(read).toHaveBeenLastCalledWith({ limit: PAGE, before: 251 }, expect.any(AbortSignal))
    expect(seqs(log())).toHaveLength(400)
    await log().loadMore()
    expect(seqs(log())).toHaveLength(450)
    expect(log().more.value).toBe(false)
  })

  // Reading past the first page is reading history, so Live stops, and what
  // arrives meanwhile waits until it is on again.
  it('turns Live off past the first page and holds what arrives', async () => {
    const { log } = harness(router(450))
    await flushPromises()
    expect(log().live.value).toBe(true)
    send({ seq: 451, word: 'new' })
    expect(seqs(log())[0]).toBe(451)

    await log().loadMore()
    expect(log().live.value).toBe(false)
    send({ seq: 452, word: 'held' })
    expect(seqs(log())[0]).toBe(451)
    log().live.value = true
    await flushPromises()
    expect(seqs(log())[0]).toBe(452)
  })

  // The router searches once typing rests; the read before it is dropped.
  it('asks the router once typing rests, and drops the read it replaces', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    let pending = 0
    const read = vi.fn(async (params, signal) => {
      pending++
      await new Promise((resolve, reject) => {
        signal.addEventListener('abort', () => reject(new DOMException('gone', 'AbortError')))
        setTimeout(resolve, 100)
      })
      return { entries: [{ seq: 1, word: params.q ?? '' }], held: 1 }
    })
    const { log } = harness(read)
    log().query.value = 'a'
    await nextTick()
    log().query.value = 'ab'
    await nextTick()
    vi.advanceTimersByTime(SETTLE_MS - 1)
    expect(read).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(1)
    expect(read).toHaveBeenCalledTimes(2)
    expect(read).toHaveBeenLastCalledWith({ limit: PAGE, q: 'ab' }, expect.any(AbortSignal))
    await vi.advanceTimersByTimeAsync(200)
    expect(pending).toBe(2)
    // The first read was dropped, not shown, and no error came of it.
    expect(log().rows.value).toEqual([{ seq: 1, word: 'ab' }])
    expect(log().error.value).toBe('')
  })

  // A streamed row lands only when the view and the search keep it, and
  // one the first read already holds does not land twice.
  // A search that stopped on its budget reads on by itself; that is not
  // scrolling into history, so Live stays on.
  it('keeps Live on while a search carries on', async () => {
    const read = vi.fn(async (params) =>
      params.before
        ? { entries: [{ seq: 5, word: 'x' }], more: false, held: 1000 }
        : { entries: [{ seq: 900, word: 'x' }], more: true, next: 600, held: 1000 },
    )
    const { log } = harness(read)
    await flushPromises()
    await log().loadMore()
    expect(seqs(log())).toEqual([900, 5])
    expect(log().live.value).toBe(true)
  })

  it('filters what arrives as the router would', async () => {
    const keep = (e) => e.word !== 'hidden'
    const { log } = harness(router(3), { keep })
    await flushPromises()
    send({ seq: 3, word: 'w3' })
    send({ seq: 4, word: 'hidden' })
    send({ seq: 5, word: 'shown' })
    expect(seqs(log())).toEqual([5, 3, 2, 1])
    log().query.value = 'zzz'
    send({ seq: 6, word: 'nope' })
    expect(seqs(log())[0]).toBe(5)
  })

  it('reads again when a select changes', async () => {
    const read = router(10)
    const verdict = ref('')
    harness(read, { filters: () => (verdict.value ? { verdict: verdict.value } : {}) })
    await flushPromises()
    verdict.value = 'blocked'
    await flushPromises()
    expect(read).toHaveBeenLastCalledWith(
      { verdict: 'blocked', limit: PAGE },
      expect.any(AbortSignal),
    )
  })

  // Live for hours on a busy router would hold every row it was sent. Past
  // the ceiling the oldest go, and can be read again.
  it('keeps the table to its ceiling while Live adds to it', async () => {
    const { log } = harness(router(MAX_ROWS))
    await flushPromises()
    while (log().more.value) await log().loadMore()
    log().live.value = true
    await flushPromises()
    expect(seqs(log())).toHaveLength(MAX_ROWS)
    send({ seq: MAX_ROWS + 1, word: 'x' })
    expect(seqs(log())).toHaveLength(MAX_ROWS)
    expect(log().more.value).toBe(true)
  })

  // A restarted router numbers its entries from 1 again, so the page
  // starts over when the stream comes back rather than wait past the
  // newest it saw.
  it('reads again and takes new numbers when the stream reopens', async () => {
    let log = router(500)
    const read = vi.fn((params, signal) => log(params, signal))
    const { log: l } = harness(read)
    await flushPromises()
    source.onopen()
    send({ seq: 501, word: 'before' })
    expect(seqs(l())[0]).toBe(501)

    // Dropped, and the browser retries.
    source.readyState = 0
    source.onerror()
    expect(l().streamError.value).toBe('Stream disconnected, retrying…')
    log = router(3)
    source.onopen()
    await flushPromises()
    expect(read).toHaveBeenCalledTimes(2)
    expect(seqs(l())).toEqual([3, 2, 1])
    send({ seq: 4, word: 'after' })
    expect(seqs(l())[0]).toBe(4)
  })

  it('says a read that failed, and closes the stream when it goes', async () => {
    const read = vi.fn().mockRejectedValue(new Error('not root'))
    const { wrapper, log } = harness(read)
    await flushPromises()
    expect(log().error.value).toBe('not root')
    wrapper.unmount()
    expect(source.closed).toBe(true)
  })
})

describe('heldLine', () => {
  it('says how much the log holds and from when', () => {
    expect(heldLine(0)).toBe('No entries.')
    expect(heldLine(1)).toBe('1 entry.')
    expect(heldLine(48213, '2026-09-25T14:02:00Z')).toMatch(/^48,213 entries back to .+\.$/)
    // Kept in files, the page reads on past what memory holds.
    expect(heldLine(48213, '2026-09-25T14:02:00Z', true)).toMatch(
      /^48,213 entries in memory back to .+\.$/,
    )
    expect(heldLine(1, '', true)).toBe('1 entry in memory.')
  })
})
