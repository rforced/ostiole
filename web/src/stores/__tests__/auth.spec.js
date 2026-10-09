import { describe, expect, it } from 'vitest'

import { ADMIN_ONLY, adminOnly } from '@/stores/auth'

describe('adminOnly', () => {
  it('says what only an admin can do, one way', () => {
    expect(adminOnly('clear', 'it')).toBe('Only an admin can clear it.')
    expect(adminOnly('log', 'the router out')).toBe('Only an admin can log the router out.')
    expect(ADMIN_ONLY).toBe('Only an admin can change this.')
  })
})
