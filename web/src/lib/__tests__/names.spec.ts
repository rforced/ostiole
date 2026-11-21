import { describe, expect, it } from 'vitest'

import { nameProblem, proxyNameProblem } from '@/lib/names'

describe('nameProblem', () => {
  it.each([
    ['name', 'wan1', true],
    ['name', 'a'.repeat(31), true],
    ['name', 'a'.repeat(32), false],
    ['name', '1wan', false],
    ['name', 'Wan', false],
    ['name', 'wan-1', false],
    ['id', 'Example.test_1-a', true],
    ['id', 'a'.repeat(40), true],
    ['id', 'a'.repeat(41), false],
    ['id', '.dot', false],
    ['id', 'with space', false],
    ['certificate', 'example-test', true],
    ['certificate', 'a'.repeat(32), true],
    ['certificate', 'a'.repeat(33), false],
    ['certificate', 'example.test', false],
    ['certificate', '-mail', false],
  ] as const)('%s %s accepted: %s', (kind, name, ok) => {
    expect(nameProblem(kind, name) === '').toBe(ok)
  })
})

describe('proxyNameProblem', () => {
  const proxy = { pools: [{ id: 'app' }], sites: [{ id: 'web' }], wafProfiles: [{ id: 'strict' }] }

  it('names the list that holds the name, whichever kind asks', () => {
    expect(proxyNameProblem(proxy, 'app')).toBe('Pool app already exists.')
    expect(proxyNameProblem(proxy, 'web')).toBe('Site web already exists.')
    expect(proxyNameProblem(proxy, 'strict')).toBe('Profile strict already exists.')
  })

  it('lets an item keep its own name', () => {
    expect(proxyNameProblem(proxy, 'web', 'web')).toBe('')
  })

  it('checks the pattern before the lists', () => {
    expect(proxyNameProblem(proxy, 'a b', 'a b')).toContain('Name must be at most 40')
    expect(proxyNameProblem({}, 'api')).toBe('')
  })
})
