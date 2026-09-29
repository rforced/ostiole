import { describe, expect, it } from 'vitest'

import table from '../../../../internal/sysupdate/testdata/excluded.json'
import { excluded } from '@/lib/packages'

describe('excluded', () => {
  it.each(table.cases)('answers for $name as the server does', (tc) => {
    expect(excluded(tc.name, tc.exclude)).toBe(tc.excluded)
  })
})
