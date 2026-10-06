import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import AppDialog from '@/components/AppDialog.vue'

async function opened(props) {
  mount(AppDialog, { props: { open: true, title: 'Zone lan', ...props }, attachTo: document.body })
  await flushPromises()
  return document.querySelector('[role="dialog"]')
}

describe('AppDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))
  afterEach(() => {
    document.body.innerHTML = ''
  })

  // reka-ui sets aria-describedby whether or not there is a description, and
  // one that names no element is invalid ARIA.
  it('is described by its description, and by nothing without one', async () => {
    const described = await opened({ description: 'Its interfaces stay where they are.' })
    const id = described.getAttribute('aria-describedby')
    expect(document.getElementById(id)?.textContent.trim()).toBe(
      'Its interfaces stay where they are.',
    )
    document.body.innerHTML = ''

    const bare = await opened({})
    expect(bare.hasAttribute('aria-describedby')).toBe(false)
  })
})
