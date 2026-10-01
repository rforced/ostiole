import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import LinkErrors from '@/components/LinkErrors.vue'

const line = (link) => mount(LinkErrors, { props: { link } })

describe('LinkErrors', () => {
  it('counts both ways together, over the last 72 hours', () => {
    const w = line({ name: 'eth0', rxErrors: 2, txErrors: 1 })
    expect(w.text()).toBe('3 errors in the last 72\u00a0hours')
    expect(w.classes()).toContain('text-warn')
  })

  it('says one error', () => {
    expect(line({ name: 'eth0', txErrors: 1 }).text()).toBe('1 error in the last 72\u00a0hours')
  })

  it('says nothing for a link without errors, or without a link', () => {
    expect(line({ name: 'eth0' }).find('div').exists()).toBe(false)
    expect(line(null).find('div').exists()).toBe(false)
  })
})
