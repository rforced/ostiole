import { describe, expect, it } from 'vitest'
import { ref } from 'vue'

import { matches, useSearch } from '@/lib/search'

describe('matches', () => {
  it('finds every word somewhere in the row, case aside', () => {
    const row = ['10.0.0.20', 'Printer', 'lan0']
    expect(matches('printer', row)).toBe(true)
    expect(matches('PRINTER lan0', row)).toBe(true)
    expect(matches('printer wan0', row)).toBe(false)
    expect(matches('   ', row)).toBe(true)
  })

  // A MAC copied from Windows or a switch has other separators.
  it('matches a MAC whatever its separators', () => {
    const macs = ['aa:bb:cc:dd:ee:01']
    expect(matches('aa-bb-cc', [], macs)).toBe(true)
    expect(matches('aabb.ccdd', [], macs)).toBe(true)
    expect(matches('AABBCCDDEE01', [], macs)).toBe(true)
    expect(matches('dd:ee', [], macs)).toBe(true)
    expect(matches('aabb.ccff', [], macs)).toBe(false)
  })

  // A port, an address or a short hex word is not written as a MAC.
  it('does not read other words as part of a MAC', () => {
    const macs = ['01:00:02:00:00:05']
    expect(matches('00', [], macs)).toBe(true)
    expect(matches('1000', [], macs)).toBe(false)
    expect(matches('10.0.0.5', [], macs)).toBe(false)
    expect(matches('2000', [], macs)).toBe(false)
    expect(matches('a-b', [], ['0a:0b:00:00:00:01'])).toBe(false)
  })

  it('skips empty values', () => {
    expect(matches('undefined', [undefined, null, ''])).toBe(false)
  })
})

describe('useSearch', () => {
  it('narrows the rows as the query changes', () => {
    const rows = ref([
      { name: 'printer', mac: 'aa:bb:cc:00:00:01' },
      { name: 'laptop', mac: 'aa:bb:cc:00:00:02' },
    ])
    const { query, shown } = useSearch(rows, (r) => ({ values: [r.name], macs: [r.mac] }))
    expect(shown.value).toHaveLength(2)
    query.value = 'lap'
    expect(shown.value.map((r) => r.name)).toEqual(['laptop'])
    query.value = 'aabbcc000001'
    expect(shown.value.map((r) => r.name)).toEqual(['printer'])
    rows.value = [...rows.value, { name: 'printer2', mac: 'aa:bb:cc:00:00:03' }]
    query.value = 'printer'
    expect(shown.value.map((r) => r.name)).toEqual(['printer', 'printer2'])
  })
})
