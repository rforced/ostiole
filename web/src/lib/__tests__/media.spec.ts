import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { useMediaQuery } from '@/lib/media'

/** A matchMedia whose answer the test changes, as a resize would. */
type Listener = (e: { matches: boolean }) => void

function stubMatchMedia(matches: boolean) {
  const listeners = new Set<Listener>()
  const mql = {
    matches,
    addEventListener: vi.fn((_: string, fn: Listener) => listeners.add(fn)),
    removeEventListener: vi.fn((_: string, fn: Listener) => listeners.delete(fn)),
  }
  vi.stubGlobal(
    'matchMedia',
    vi.fn(() => mql),
  )
  return {
    listeners,
    change(next: boolean) {
      mql.matches = next
      for (const fn of listeners) fn({ matches: next })
    },
  }
}

describe('useMediaQuery', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('never matches without matchMedia', () => {
    expect(useMediaQuery('(width < 64rem)').value).toBe(false)
  })

  it('follows the window, and stops listening with its scope', () => {
    const media = stubMatchMedia(true)
    const scope = effectScope()
    const narrow = scope.run(() => useMediaQuery('(width < 64rem)'))
    expect(window.matchMedia).toHaveBeenCalledWith('(width < 64rem)')
    expect(narrow?.value).toBe(true)
    media.change(false)
    expect(narrow?.value).toBe(false)
    scope.stop()
    expect(media.listeners.size).toBe(0)
  })
})
