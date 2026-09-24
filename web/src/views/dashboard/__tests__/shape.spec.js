import { beforeEach, describe, expect, it } from 'vitest'

import {
  FIRST_SHAPE,
  SHAPE_KEY,
  heightsIn,
  rememberShape,
  rememberedShape,
  shapeOf,
} from '@/views/dashboard/shape'

describe('shapeOf', () => {
  it('counts the rows and names the cards an overview brings', () => {
    const shape = shapeOf({
      warnings: [{}, {}],
      interfaces: [{}, {}, {}],
      topRules: [{}],
      services: [{}, {}],
      gateways: [],
      unwatchedGateways: [{}],
      recentBlocks: [],
      dhcp: { enabled: true },
      recentLeases: [{}, {}],
      wireless: { clients: [{}] },
      blocking: { enabled: false },
    })
    expect(shape).toEqual({
      warnings: 2,
      interfaces: 3,
      rules: 1,
      services: 2,
      // An empty block list still has its card; blocking that is off has none.
      cards: { gateways: 1, blocks: 0, leases: 2, wireless: 1 },
    })
  })

  it('leaves out every card an overview has nothing for', () => {
    expect(shapeOf({}).cards).toEqual({})
  })
})

describe('heightsIn', () => {
  it('measures each named block and skips what has no height', () => {
    const root = document.createElement('div')
    root.innerHTML = '<section data-card="rules"></section><section data-card="router"></section>'
    Object.defineProperty(root.firstElementChild, 'offsetHeight', { value: 241 })
    expect(heightsIn(root)).toEqual({ rules: 241 })
  })
})

describe('rememberedShape', () => {
  beforeEach(() => localStorage.clear())

  it('is the first-visit shape until something is kept', () => {
    expect(rememberedShape()).toBe(FIRST_SHAPE)
    localStorage.setItem(SHAPE_KEY, '{nonsense')
    expect(rememberedShape()).toBe(FIRST_SHAPE)
  })

  it('takes only sensible counts, known cards and plausible heights from storage', () => {
    localStorage.setItem(
      SHAPE_KEY,
      JSON.stringify({
        warnings: -1,
        interfaces: 500,
        rules: '2',
        cards: { gateways: 1.5, leases: 2, bogus: 3 },
        heights: { interfaces: 290, rules: 0, router: 99999, bogus: 10, services: '241' },
      }),
    )
    expect(rememberedShape()).toEqual({
      warnings: 0,
      interfaces: 20,
      rules: 3,
      services: 3,
      cards: { gateways: 0, leases: 2 },
      heights: { interfaces: 290 },
    })
  })
})

describe('rememberShape', () => {
  beforeEach(() => localStorage.clear())

  it('keeps a shape for the next visit', () => {
    const shape = {
      ...shapeOf({ interfaces: [{}], dhcp: { enabled: true } }),
      heights: { leases: 187 },
    }
    rememberShape(shape)
    expect(rememberedShape()).toEqual({
      warnings: 0,
      interfaces: 1,
      rules: 0,
      services: 0,
      cards: { leases: 0 },
      heights: { leases: 187 },
    })
  })
})
