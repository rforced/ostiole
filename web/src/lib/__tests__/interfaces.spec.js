import { describe, expect, it } from 'vitest'

import { facesInternet } from '@/lib/interfaces'

import cases from './faces-internet.json'

describe('facesInternet', () => {
  // The server reads the same cases, so the two cannot drift.
  it.each(Object.entries(cases.facesInternet))('%s faces the internet: %s', (name, want) => {
    expect(facesInternet(cases.config, name)).toBe(want)
  })

  it('reads nothing into a missing configuration', () => {
    expect(facesInternet(null, 'eth0')).toBe(false)
  })
})
