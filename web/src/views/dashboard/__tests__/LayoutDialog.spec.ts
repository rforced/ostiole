import { type VueWrapper, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { DEFAULT_ORDER, type Layout, firstLayout } from '@/views/dashboard/layout'
import LayoutDialog from '@/views/dashboard/LayoutDialog.vue'

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
  'transition-group': false,
}

function open(layout: Layout = firstLayout()) {
  return mount(LayoutDialog, { props: { open: true, layout }, global: { stubs } })
}

const titles = (w: VueWrapper) => w.findAll('li').map((li) => li.text())
const row = (w: VueWrapper, title: string) => {
  const found = w.findAll('li').find((li) => li.text() === title)
  if (!found) throw new Error(`no row ${title}`)
  return found
}
const restore = (w: VueWrapper) => w.findAll('button').find((b) => b.text() === 'Restore')
const changes = (w: VueWrapper) => (w.emitted<[Layout]>('change') ?? []).map(([layout]) => layout)

describe('LayoutDialog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('lists every card in the layout order, with a switch for each', () => {
    const w = open({ order: ['router', ...DEFAULT_ORDER.slice(0, -1)], hidden: ['wireless'] })
    expect(titles(w)).toHaveLength(10)
    expect(titles(w).slice(0, 2)).toEqual(['Router', 'System'])
    expect(row(w, 'Wireless').get<HTMLInputElement>('[role=switch]').element.checked).toBe(false)
    expect(row(w, 'Services').get<HTMLInputElement>('[role=switch]').element.checked).toBe(true)
    expect(row(w, 'Services').get('[role=switch]').attributes('aria-label')).toBe('Show Services')
  })

  it('hides a card at once, and shows it again', async () => {
    const w = open()
    await row(w, 'Services').get('[role=switch]').setValue(false)
    await row(w, 'Services').get('[role=switch]').setValue(true)
    expect(changes(w)).toEqual([
      { order: [...DEFAULT_ORDER], hidden: ['services'] },
      { order: [...DEFAULT_ORDER], hidden: [] },
    ])
  })

  it('offers Restore only while the layout is not the default', async () => {
    const w = open()
    expect(restore(w)).toBeUndefined()
    await row(w, 'Router').get('[role=switch]').setValue(false)
    await restore(w)?.trigger('click')
    expect(changes(w).at(-1)).toEqual(firstLayout())
    expect(restore(w)).toBeUndefined()
  })

  describe('a drag', () => {
    /** Rows 40 pixels tall from the top, so their centres are 20, 60, 100… */
    function layOut(w: VueWrapper) {
      vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (
        this: HTMLElement,
      ) {
        const i = [...(this.parentElement?.children ?? [])].indexOf(this)
        return { top: i * 40, height: 40 } as DOMRect
      })
      const list = w.get<HTMLElement>('ul').element
      const capture = vi.fn()
      list.setPointerCapture = capture
      list.hasPointerCapture = () => true
      list.releasePointerCapture = vi.fn()
      return { list, capture }
    }
    const pointer = (el: EventTarget, type: string, clientY: number, init: PointerEventInit = {}) =>
      el.dispatchEvent(new PointerEvent(type, { pointerId: 1, clientY, ...init }))
    const grab = (w: VueWrapper, title: string, clientY: number, pointerType = 'mouse') =>
      pointer(row(w, title).get('[data-handle]').element, 'pointerdown', clientY, {
        pointerType,
        button: 0,
        bubbles: true,
        cancelable: true,
      })

    it('moves the row to the slot nearest it and saves once, when it lets go', async () => {
      const w = open()
      const { list, capture } = layOut(w)
      grab(w, 'System', 25)
      expect(capture).toHaveBeenCalledWith(1)

      pointer(list, 'pointermove', 70)
      pointer(list, 'pointermove', 105)
      await flushPromises()
      expect(titles(w).slice(0, 3)).toEqual(['Gateways', 'Traffic', 'System'])
      expect(changes(w)).toEqual([])

      pointer(list, 'pointerup', 105)
      await flushPromises()
      expect(changes(w)).toEqual([
        { order: ['gateways', 'traffic', 'system', ...DEFAULT_ORDER.slice(3)], hidden: [] },
      ])
    })

    it('puts the rows back and saves nothing when the pointer is cancelled', async () => {
      const w = open()
      const { list } = layOut(w)
      const before = titles(w)
      grab(w, 'Router', 380, 'touch')
      pointer(list, 'pointermove', 20)
      await flushPromises()
      expect(titles(w)[0]).toBe('Router')

      pointer(list, 'pointercancel', 20)
      await flushPromises()
      expect(titles(w)).toEqual(before)
      expect(changes(w)).toEqual([])
    })

    it('saves nothing when the row lands where it was', async () => {
      const w = open()
      const { list } = layOut(w)
      grab(w, 'Traffic', 100)
      pointer(list, 'pointermove', 115)
      pointer(list, 'pointerup', 115)
      await flushPromises()
      expect(changes(w)).toEqual([])
    })
  })
})
