import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import MenuButton from '@/components/MenuButton.vue'

const ITEMS = [
  { label: 'Full chain', href: '/files/fullchain.pem' },
  { label: 'Certificate', href: '/files/cert.pem' },
]

async function menu(items = ITEMS) {
  const wrapper = mount(MenuButton, {
    props: { label: 'Download', items },
    attrs: { class: 'ml-3' },
    attachTo: document.body,
  })
  await wrapper.get('button').trigger('click')
  await flushPromises()
  return wrapper
}

const entries = () => [...document.querySelectorAll('[role="menuitem"]')]

describe('MenuButton', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('opens a menu of its items, links as links', async () => {
    const wrapper = await menu()
    const trigger = wrapper.get('button')
    expect(trigger.text()).toBe('Download')
    expect(trigger.classes()).toContain('ml-3')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(document.querySelector('[role="menu"]').getAttribute('aria-labelledby')).toBe(
      trigger.attributes('id'),
    )
    const shown = entries().map((e) => [e.tagName, e.textContent.trim(), e.getAttribute('href')])
    expect(shown).toEqual([
      ['A', 'Full chain', '/files/fullchain.pem'],
      ['A', 'Certificate', '/files/cert.pem'],
    ])
  })

  // A dialog gives focus back to whatever had it when it opened, and the
  // item is gone by the time the dialog closes.
  it('runs an action once the menu has closed, from the trigger', async () => {
    let seen = null
    const action = vi.fn(() => {
      seen = document.activeElement
    })
    const wrapper = await menu([...ITEMS, { label: 'PKCS#12', action }])
    const item = entries().at(-1)
    expect(item.tagName).toBe('DIV')
    item.click()
    await flushPromises()
    expect(action).toHaveBeenCalledOnce()
    expect(seen).toBe(wrapper.get('button').element)
    expect(entries()).toHaveLength(0)
    expect(wrapper.get('button').attributes('aria-expanded')).toBe('false')
  })
})
