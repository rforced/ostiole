import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import { emptyText, errorMessage, useAsync } from '@/lib/async'

/** A promise whose fate the test decides. */
function deferred() {
  let resolve, reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('useAsync', () => {
  it('tracks busy, the last success, and the result', async () => {
    const d = deferred()
    const load = useAsync(() => d.promise)
    expect(load.busy.value).toBe(false)
    expect(load.updatedAt.value).toBe(0)

    const out = load.run()
    expect(load.busy.value).toBe(true)
    d.resolve('rows')
    await expect(out).resolves.toBe('rows')
    expect(load.busy.value).toBe(false)
    expect(load.updatedAt.value).toBeGreaterThan(0)
    expect(load.error.value).toBe('')
  })

  it('keeps a failure in error rather than throwing', async () => {
    const load = useAsync(() => Promise.reject(new Error('no route to host')))
    await expect(load.run()).resolves.toBeUndefined()
    expect(load.error.value).toBe('no route to host')
    expect(load.busy.value).toBe(false)
    expect(load.updatedAt.value).toBe(0)
  })

  it('runs again after a loader that throws before it awaits', async () => {
    const fn = vi.fn(() => {
      throw new Error('no such interface')
    })
    const load = useAsync(fn)
    await load.run()
    expect(load.error.value).toBe('no such interface')
    fn.mockResolvedValue('rows')
    await expect(load.run()).resolves.toBe('rows')
    expect(fn).toHaveBeenCalledTimes(2)
    expect(load.busy.value).toBe(false)
  })

  it('clears the error on the next success', async () => {
    let fail = true
    const load = useAsync(async () => {
      if (fail) throw new Error('down')
      return 'up'
    })
    await load.run()
    expect(load.error.value).toBe('down')
    fail = false
    await load.run()
    expect(load.error.value).toBe('')
  })

  it('shares a run in flight and runs once more afterwards', async () => {
    const fn = vi.fn()
    const first = deferred()
    fn.mockReturnValueOnce(first.promise).mockResolvedValue('second')
    const load = useAsync(fn)

    const a = load.run()
    const b = load.run()
    const c = load.run()
    expect(b).toBe(a)
    expect(c).toBe(a)
    expect(fn).toHaveBeenCalledTimes(1)

    first.resolve('first')
    await a
    // The callers that arrived mid-flight got one follow-up between them.
    await vi.waitFor(() => expect(fn).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => expect(load.busy.value).toBe(false))
  })

  it('passes the latest arguments to the follow-up run', async () => {
    const fn = vi.fn()
    const first = deferred()
    fn.mockReturnValueOnce(first.promise).mockResolvedValue(undefined)
    const load = useAsync(fn)
    load.run('a')
    load.run('b')
    load.run('c')
    first.resolve()
    await vi.waitFor(() => expect(fn).toHaveBeenCalledTimes(2))
    expect(fn).toHaveBeenLastCalledWith('c')
  })
})

describe('errorMessage', () => {
  it('reads an Error and stringifies the rest', () => {
    expect(errorMessage(new Error('boom'))).toBe('boom')
    expect(errorMessage('plain')).toBe('plain')
  })
})

describe('emptyText', () => {
  it('reads until the first read lands, then says the list is empty', () => {
    const load = { updatedAt: ref(0), error: ref('') }
    expect(emptyText(load, 'No leases.')).toBe('Reading…')
    load.updatedAt.value = Date.now()
    expect(emptyText(load, 'No leases.')).toBe('No leases.')
  })

  // The error says why; Reading… would claim a read that is not happening.
  it('stops reading once the first read fails', () => {
    const load = { updatedAt: ref(0), error: ref('') }
    load.error.value = 'The firewall log needs the daemon to run as root.'
    expect(emptyText(load, 'No packets.')).toBe('No packets.')
    expect(emptyText({ updatedAt: ref(0) }, 'No time servers.')).toBe('Reading…')
  })
})
