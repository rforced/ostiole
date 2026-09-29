import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SearchBox from '@/components/SearchBox.vue'

describe('SearchBox', () => {
  it('is a labelled search field bound to its model', async () => {
    const wrapper = mount(SearchBox, {
      props: {
        modelValue: '',
        'onUpdate:modelValue': (v) => wrapper.setProps({ modelValue: v }),
        placeholder: 'address, MAC, or interface',
      },
    })
    const input = wrapper.get('input[type=search]')
    expect(wrapper.get(`label[for="${input.attributes('id')}"]`).text()).toBe('Search')
    expect(input.attributes('placeholder')).toBe('address, MAC, or interface')
    await input.setValue('printer')
    expect(wrapper.props('modelValue')).toBe('printer')
  })

  it('counts the rows left when it knows how many there are', async () => {
    const wrapper = mount(SearchBox, { props: { placeholder: 'x', shown: 2, total: 5 } })
    expect(wrapper.text()).toContain('2 of 5')
    await wrapper.setProps({ total: null })
    expect(wrapper.text()).not.toContain('of')
  })
})
