import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { h } from 'vue'

import SectionCard from '@/components/SectionCard.vue'

function card(props, footer) {
  return mount(SectionCard, {
    props: { title: 'Widgets', ...props },
    slots: {
      default: () => h('p', { id: 'body' }, 'Body'),
      ...(footer ? { footer: () => 'Only an admin can change this.' } : {}),
    },
  })
}

describe('SectionCard footer', () => {
  it.each([false, true])('is a strip after the body, flush %s', (flush) => {
    const w = card({ flush }, true)
    const strip = w.find('.card-strip')
    expect(strip.text()).toBe('Only an admin can change this.')
    expect(strip.classes()).toContain('border-t')
    expect(w.element.lastElementChild).toBe(strip.element)
  })

  it('stays outside a locked body', () => {
    const w = card({ locked: true }, true)
    expect(w.find('fieldset').find('.card-strip').exists()).toBe(false)
    expect(w.find('fieldset #body').exists()).toBe(true)
  })

  it('renders nothing without one', () => {
    expect(card({}, false).find('.card-strip').exists()).toBe(false)
  })
})
