import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import RandomMacBadge from '@/components/RandomMacBadge.vue'

describe('RandomMacBadge', () => {
  it('marks a MAC the device made up, and says what that means', () => {
    const badge = mount(RandomMacBadge, { props: { mac: '02:00:5e:00:53:10' } }).get('.badge')
    expect(badge.text()).toBe('random')
    expect(badge.attributes('title')).toBe('The device may change this address.')
  })

  it('says nothing of a maker’s MAC, a fixed made-up one, or none', () => {
    for (const mac of ['00:00:5e:00:53:60', '52:54:00:12:34:56', '', undefined]) {
      expect(mount(RandomMacBadge, { props: { mac } }).find('.badge').exists()).toBe(false)
    }
  })
})
