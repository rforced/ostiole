import { describe, expect, it } from 'vitest'

import { createRateTracker } from '@/lib/rates'

const link = (name, rxBytes, txBytes) => ({ name, rxBytes, txBytes })

describe('createRateTracker', () => {
  it('needs two samples before it has a rate', () => {
    const sample = createRateTracker()
    expect(sample([link('wan', 1000, 500)], 0)).toEqual({})
    expect(sample([link('wan', 11_000, 1500)], 10_000)).toEqual({
      wan: { rx: 8000, tx: 800 },
    })
  })

  it('keeps the last rate and baseline when a sample comes too soon', () => {
    const sample = createRateTracker()
    sample([link('wan', 0, 0)], 0)
    sample([link('wan', 10_000, 0)], 10_000)
    expect(sample([link('wan', 10_500, 0)], 10_200)).toEqual({ wan: { rx: 8000, tx: 0 } })
    // The baseline stayed at the 10 s mark, so the next window is 10 s again.
    expect(sample([link('wan', 20_000, 0)], 20_000)).toEqual({ wan: { rx: 8000, tx: 0 } })
  })

  it('starts over when a counter goes backwards', () => {
    const sample = createRateTracker()
    sample([link('wan', 5000, 5000)], 0)
    expect(sample([link('wan', 100, 100)], 10_000)).toEqual({})
    expect(sample([link('wan', 1100, 100)], 20_000)).toEqual({ wan: { rx: 800, tx: 0 } })
  })

  it('forgets a link that disappears', () => {
    const sample = createRateTracker()
    sample([link('wan', 0, 0), link('lan', 0, 0)], 0)
    expect(sample([link('wan', 1000, 0)], 10_000)).toEqual({ wan: { rx: 800, tx: 0 } })
    expect(sample([link('wan', 2000, 0), link('lan', 9000, 0)], 20_000)).toEqual({
      wan: { rx: 800, tx: 0 },
    })
  })
})
