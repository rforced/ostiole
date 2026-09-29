import { beforeEach, describe, expect, it, vi } from 'vitest'

import { RELOAD_KEY, RELOAD_WINDOW_MS, installChunkReload } from '@/lib/reload'

/** A preload failure, as Vite raises it. */
function preloadError() {
  return new Event('vite:preloadError', { cancelable: true })
}

describe('installChunkReload', () => {
  let target
  let reload

  beforeEach(() => {
    sessionStorage.clear()
    target = new EventTarget()
    reload = vi.fn()
  })

  it('reloads when a chunk the running build asks for is gone', () => {
    installChunkReload(target, reload)
    const event = preloadError()
    target.dispatchEvent(event)
    expect(reload).toHaveBeenCalledTimes(1)
    // Vite rethrows otherwise, and the router reports a navigation failure
    // the reload is about to make irrelevant.
    expect(event.defaultPrevented).toBe(true)
  })

  it('answers only the first of several chunks failing at once', () => {
    installChunkReload(target, reload)
    target.dispatchEvent(preloadError())
    target.dispatchEvent(preloadError())
    target.dispatchEvent(preloadError())
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('does not reload again when the new build fails the same way', () => {
    sessionStorage.setItem(RELOAD_KEY, String(Date.now()))
    installChunkReload(target, reload)
    target.dispatchEvent(preloadError())
    expect(reload).not.toHaveBeenCalled()
  })

  it('reloads again once the last one is old enough to be unrelated', () => {
    sessionStorage.setItem(RELOAD_KEY, String(Date.now() - RELOAD_WINDOW_MS - 1))
    installChunkReload(target, reload)
    target.dispatchEvent(preloadError())
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('still reloads where there is no storage to remember it in', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage unavailable')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('storage unavailable')
    })
    installChunkReload(target, reload)
    target.dispatchEvent(preloadError())
    expect(reload).toHaveBeenCalledTimes(1)
    vi.restoreAllMocks()
  })
})
