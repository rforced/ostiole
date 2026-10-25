import { describe, expect, it } from 'vitest'

import { nearest } from '@/lib/dragSort'

describe('nearest', () => {
  const centres = [20, 60, 100, 140]

  it('is the slot whose centre is closest', () => {
    expect(nearest(20, centres)).toBe(0)
    expect(nearest(39, centres)).toBe(0)
    expect(nearest(41, centres)).toBe(1)
    expect(nearest(110, centres)).toBe(2)
  })

  it('is the first above the list and the last below it', () => {
    expect(nearest(-500, centres)).toBe(0)
    expect(nearest(900, centres)).toBe(3)
  })
})
