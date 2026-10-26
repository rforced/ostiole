import { describe, expect, it } from 'vitest'
import { ref } from 'vue'

import { matches, useSearch } from '@/lib/search'

import cases from './search-cases.json'

// One table of cases, which internal/logsearch reads as well: the router
// searches a log the way the page searches a list it holds.
describe('matches', () => {
  for (const c of cases) {
    it(c.why, () => {
      expect(matches(c.query, c.values, c.macs ?? [])).toBe(c.match)
    })
  }

  it('skips values that are not there', () => {
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
