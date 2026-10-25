import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  CARDS,
  DEFAULT_ORDER,
  LAYOUT_KEY,
  firstLayout,
  isCustomized,
  loadLayout,
  normalizeLayout,
  saveLayout,
} from '@/views/dashboard/layout'

describe('normalizeLayout', () => {
  it('is the first layout for anything that is not an object', () => {
    expect(normalizeLayout(null)).toEqual(firstLayout())
    expect(normalizeLayout('nope')).toEqual(firstLayout())
    expect(normalizeLayout(3)).toEqual(firstLayout())
  })

  it('keeps a stored order and drops names this build does not know', () => {
    expect(normalizeLayout({ order: ['router', 'bogus', 'system'], hidden: [] }).order).toEqual([
      'router',
      'system',
      ...DEFAULT_ORDER.filter((n) => n !== 'router' && n !== 'system'),
    ])
  })

  it('appends a known name the stored order has lost, where the default puts it', () => {
    const order = normalizeLayout({ order: ['router'], hidden: [] }).order
    expect(order).toHaveLength(CARDS.length)
    expect(order).toEqual(['router', ...DEFAULT_ORDER.filter((n) => n !== 'router')])
  })

  it('removes a name the order repeats', () => {
    expect(normalizeLayout({ order: ['system', 'system', 'router'], hidden: [] }).order).toEqual([
      'system',
      'router',
      ...DEFAULT_ORDER.filter((n) => n !== 'system' && n !== 'router'),
    ])
  })

  it('keeps only hidden names the order holds', () => {
    expect(
      normalizeLayout({ order: ['router', 'system'], hidden: ['wireless', 'bogus'] }).hidden,
    ).toEqual(['wireless'])
    expect(normalizeLayout({ hidden: ['wireless'] })).toEqual(
      expect.objectContaining({ order: [...DEFAULT_ORDER], hidden: ['wireless'] }),
    )
  })

  it('removes a name hidden twice', () => {
    expect(normalizeLayout({ hidden: ['router', 'router'] }).hidden).toEqual(['router'])
  })

  it('is not customized while it is the first layout', () => {
    expect(isCustomized(firstLayout())).toBe(false)
    expect(
      isCustomized({
        order: ['router', ...DEFAULT_ORDER.filter((n) => n !== 'router')],
        hidden: [],
      }),
    ).toBe(true)
    expect(isCustomized({ order: [...DEFAULT_ORDER], hidden: ['wireless'] })).toBe(true)
  })
})

describe('loadLayout and saveLayout', () => {
  beforeEach(() => localStorage.clear())

  it('round-trips a layout for the next visit', () => {
    const layout = {
      order: ['router', ...DEFAULT_ORDER.filter((n) => n !== 'router')],
      hidden: ['wireless'],
    }
    saveLayout(layout)
    expect(loadLayout()).toEqual(layout)
  })

  it('reads the first layout until something is kept, and for junk', () => {
    expect(loadLayout()).toEqual(firstLayout())
    localStorage.setItem(LAYOUT_KEY, '{nonsense')
    expect(loadLayout()).toEqual(firstLayout())
    localStorage.setItem(LAYOUT_KEY, JSON.stringify({ order: 'nope', hidden: 'nope' }))
    expect(loadLayout()).toEqual(firstLayout())
  })

  it('keeps what it held when storage refuses the write', () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(() => saveLayout(firstLayout())).not.toThrow()
    setItem.mockRestore()
  })
})
