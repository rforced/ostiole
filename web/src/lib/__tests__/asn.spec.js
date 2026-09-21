import { describe, expect, it } from 'vitest'

import { canonicalAsn, formatAsn, parseAsn, parseAsnList } from '@/lib/asn'

describe('parseAsn', () => {
  it('reads the three spellings', () => {
    expect(parseAsn('AS15169')).toBe(15169)
    expect(parseAsn('as15169')).toBe(15169)
    expect(parseAsn(' 15169 ')).toBe(15169)
    expect(parseAsn('AS4294967295')).toBe(4294967295)
  })

  it('rejects zero, junk and overflow', () => {
    for (const bad of [
      '',
      'AS',
      '0',
      'AS0',
      '-1',
      'google',
      'AS15169x',
      'AS 15169',
      'AS4294967296',
    ]) {
      expect(parseAsn(bad), bad).toBeNull()
    }
  })

  it('writes one spelling', () => {
    expect(formatAsn(15169)).toBe('AS15169')
    expect(canonicalAsn('15169')).toBe('AS15169')
    expect(canonicalAsn('google')).toBe('google')
  })
})

describe('parseAsnList', () => {
  it('normalises, drops repeats and reports junk', () => {
    expect(parseAsnList('as15169\n15169, AS4134 google\n')).toEqual({
      asns: ['AS15169', 'AS4134'],
      invalid: ['google'],
    })
    expect(parseAsnList('')).toEqual({ asns: [], invalid: [] })
  })
})
