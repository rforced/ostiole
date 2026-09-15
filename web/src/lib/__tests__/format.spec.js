import { describe, expect, it } from 'vitest'

import { formatBytes, formatCount } from '@/lib/format'

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
