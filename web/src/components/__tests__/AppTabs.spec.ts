import type { VueWrapper } from '@vue/test-utils'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { nextTick } from 'vue'

import AppTabs from '@/components/AppTabs.vue'

const TABS = ['a', 'b', 'c'].map((value) => ({ value, label: value.toUpperCase() }))

const rect = (left: number, width: number): DOMRect => ({
  left,
  width,
  x: left,
  y: 0,
  top: 0,
  right: left + width,
  bottom: 0,
  height: 0,
  toJSON: () => ({}),
})

/**
 * jsdom has no layout, so give the strip and its tabs the boxes a phone
 * would: three 100px tabs in a 100px strip, or a strip wide enough.
 */
function lay(wrapper: VueWrapper, width: number) {
  const strip = wrapper.get('[role="tablist"]').element
  let left = 0
  Object.defineProperties(strip, {
    clientWidth: { value: width, configurable: true },
    scrollWidth: { value: 300, configurable: true },
    scrollLeft: {
      get: () => left,
      set: (v) => {
        left = v
      },
      configurable: true,
    },
  })
  strip.getBoundingClientRect = () => rect(0, width)
  strip.querySelectorAll('[role="tab"]').forEach((tab, i: number) => {
    tab.getBoundingClientRect = () => rect(i * 100 - left, 100)
  })
  return strip
}

function tabs(modelValue: string) {
  const wrapper = mount(AppTabs, {
    props: {
      tabs: TABS,
      modelValue,
      'onUpdate:modelValue': (v: string) => wrapper.setProps({ modelValue: v }),
    },
  })
  return wrapper
}

describe('AppTabs', () => {
  it('scrolls a strip wider than the screen to the open tab', async () => {
    const wrapper = tabs('a')
    const strip = lay(wrapper, 100)
    await wrapper.setProps({ modelValue: 'c' })
    await nextTick()
    expect(strip.scrollLeft).toBe(200)
    await wrapper.setProps({ modelValue: 'b' })
    await nextTick()
    expect(strip.scrollLeft).toBe(100)
  })

  it('leaves a strip that fits alone', async () => {
    const wrapper = tabs('a')
    const strip = lay(wrapper, 300)
    await wrapper.setProps({ modelValue: 'c' })
    await nextTick()
    expect(strip.scrollLeft).toBe(0)
  })
})
