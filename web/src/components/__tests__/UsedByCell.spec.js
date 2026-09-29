import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import UsedByCell from '@/components/UsedByCell.vue'

const cell = (names) => mount(UsedByCell, { props: { names } })

describe('UsedByCell', () => {
  it('names the first and counts the rest, with all of them in the title', () => {
    const td = cell(['rule r-1', 'rule r-2', 'rule r-3'])
    expect(td.text()).toBe('rule r-1 and 2 more')
    expect(td.attributes('title')).toBe('rule r-1, rule r-2, rule r-3')
    expect(td.classes()).not.toContain('max-sm:hidden')
  })

  it('shows one name whole, with no title', () => {
    const td = cell(['router'])
    expect(td.text()).toBe('router')
    expect(td.attributes('title')).toBeUndefined()
  })

  it('shows a dash when nothing uses it, and no line on a phone', () => {
    const td = cell([])
    expect(td.text()).toBe('—')
    expect(td.attributes('data-label')).toBe('Used by')
    expect(td.classes()).toContain('max-sm:hidden')
  })
})
