import { describe, expect, it } from 'vitest'

import { keptOrder } from '@/lib/lists'

describe('keptOrder', () => {
  it('keeps the saved order for the same items', () => {
    const was = ['b', 'c', 'a']
    const kept = keptOrder(was, ['a', 'b', 'c'])
    expect(kept).toEqual(['b', 'c', 'a'])
    expect(kept).not.toBe(was)
  })

  it('takes the new list when an item is added or removed', () => {
    expect(keptOrder(['b', 'a'], ['a', 'b', 'c'])).toEqual(['a', 'b', 'c'])
    expect(keptOrder(['b', 'c', 'a'], ['a', 'c'])).toEqual(['a', 'c'])
  })

  it('takes the new list when none was saved', () => {
    expect(keptOrder(undefined, ['b', 'a'])).toEqual(['b', 'a'])
    expect(keptOrder(null, ['b', 'a'])).toEqual(['b', 'a'])
  })
})
