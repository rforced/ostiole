import { mount } from '@vue/test-utils'
import { Send } from 'lucide-vue-next'
import { describe, expect, it } from 'vitest'

import ActionButton from '@/components/ActionButton.vue'

describe('ActionButton', () => {
  it('says the action, with its icon', () => {
    const w = mount(ActionButton, {
      props: { label: 'Send a test', busyLabel: 'Sending…', icon: Send },
    })
    const b = w.get('button')
    expect(b.text()).toBe('Send a test')
    expect(b.attributes('type')).toBe('button')
    expect(b.classes()).toContain('btn-secondary')
    expect(b.attributes('aria-busy')).toBe('false')
    expect(b.element.disabled).toBe(false)
    expect(w.find('svg.animate-spin').exists()).toBe(false)
    expect(w.find('svg').exists()).toBe(true)
  })

  it('spins and says the participle while it runs', () => {
    const w = mount(ActionButton, {
      props: {
        label: 'Send a test',
        busyLabel: 'Sending…',
        busy: true,
        icon: Send,
        kind: 'primary',
      },
    })
    const b = w.get('button')
    expect(b.text()).toBe('Sending…')
    expect(b.classes()).toContain('btn-primary')
    expect(b.attributes('aria-busy')).toBe('true')
    expect(b.element.disabled).toBe(true)
    expect(w.findAll('svg')).toHaveLength(1)
    expect(w.find('svg.animate-spin').exists()).toBe(true)
  })

  it('is a row action as a link, and off for its own reasons', () => {
    const w = mount(ActionButton, {
      props: {
        label: 'Run now',
        busyLabel: 'Running…',
        kind: 'link',
        type: 'submit',
        disabled: true,
      },
    })
    const b = w.get('button')
    expect(b.classes()).toContain('link-action')
    expect(b.attributes('type')).toBe('submit')
    expect(b.element.disabled).toBe(true)
    expect(b.attributes('aria-busy')).toBe('false')
  })
})
