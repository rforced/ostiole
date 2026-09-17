import { describe, expect, it, vi } from 'vitest'

import { errorMessage, useAsync } from '@/lib/async'

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
