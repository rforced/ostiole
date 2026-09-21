import { describe, expect, it } from 'vitest'

import { formatBytes, formatCount, formatRate } from '@/lib/format'

describe('formatBytes', () => {
  it('uses decimal units and one decimal below 100', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(999)).toBe('999 B')
    expect(formatBytes(1536)).toBe('1.5 kB')
    expect(formatBytes(150_000)).toBe('150 kB')
    expect(formatBytes(2_500_000_000)).toBe('2.5 GB')
  })

  it('refuses nonsense', () => {
    expect(formatBytes(-1)).toBe('—')
    expect(formatBytes(undefined)).toBe('—')
  })
})

describe('formatCount', () => {
  it('groups thousands', () => {
    expect(formatCount(0)).toBe('0')
    expect(formatCount(12345)).toBe((12345).toLocaleString())
    expect(formatCount('nope')).toBe('—')
  })
})

describe('formatRate', () => {
  // A line is sold in bits, so that is what it is read back in.
  it('reads a line speed the way a line is sold', () => {
    expect(formatRate(0)).toBe('0 bit/s')
    expect(formatRate(64_000)).toBe('64 kbit/s')
    expect(formatRate(20_000_000)).toBe('20 Mbit/s')
    expect(formatRate(1_000_000_000)).toBe('1 Gbit/s')
    expect(formatRate(2_500_000_000)).toBe('2.5 Gbit/s')
  })

  // A rate worked out from two samples is never round, and a figure with
  // six decimals is not a reading anybody takes.
  it('keeps a measured rate short', () => {
    expect(formatRate(1_234_567)).toBe('1.23 Mbit/s')
    expect(formatRate(12_345_678)).toBe('12.3 Mbit/s')
    expect(formatRate(123_456_789)).toBe('123 Mbit/s')
  })

  it('refuses nonsense', () => {
    expect(formatRate(-1)).toBe('—')
    expect(formatRate(undefined)).toBe('—')
  })
})
