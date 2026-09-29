import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import LiveButton from '@/components/LiveButton.vue'

function live(props = {}) {
  const wrapper = mount(LiveButton, {
    props: {
      modelValue: false,
      'onUpdate:modelValue': (v) => wrapper.setProps({ modelValue: v }),
      ...props,
    },
  })
  return wrapper
}

describe('LiveButton', () => {
  it('switches on and off, and says which', async () => {
    const wrapper = live()
    const button = wrapper.get('button')
    expect(button.text()).toBe('Live')
    expect(button.attributes('aria-pressed')).toBe('false')
    expect(button.attributes('data-state')).toBe('off')

    await button.trigger('click')
    expect(wrapper.props('modelValue')).toBe(true)
    expect(button.attributes('aria-pressed')).toBe('true')
    expect(button.attributes('data-state')).toBe('live')

    await button.trigger('click')
    expect(wrapper.props('modelValue')).toBe(false)
  })

  // Amber while the stream or the reads fail; only while it is on.
  it('shows failing only while live', async () => {
    const wrapper = live({ failing: true })
    expect(wrapper.get('button').attributes('data-state')).toBe('off')
    await wrapper.setProps({ modelValue: true })
    expect(wrapper.get('button').attributes('data-state')).toBe('failing')
  })

  it('can be held off', () => {
    expect(live({ disabled: true }).get('button').attributes('disabled')).toBeDefined()
  })
})
