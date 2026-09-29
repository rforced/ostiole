import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ChoicePicker from '@/views/firewall/ChoicePicker.vue'

// What Oracle's list offers, cut down, and GitHub's keys.
const oracle = [
  { field: 'region', values: ['eu-frankfurt-1', 'us-ashburn-1', 'us-phoenix-1'] },
  { field: 'tags', values: ['OBJECT_STORAGE', 'OCI', 'OSN'] },
]
const github = [{ values: ['actions', 'hooks', 'web'] }]

function pick(choices, modelValue = []) {
  const wrapper = mount(ChoicePicker, {
    props: {
      choices,
      modelValue,
      'onUpdate:modelValue': (v) => wrapper.setProps({ modelValue: v }),
    },
  })
  return wrapper
}

/** The checkbox beside a value. */
function box(wrapper, value) {
  const label = wrapper.findAll('label').find((l) => l.text() === value)
  return label.get('input[type=checkbox]')
}

describe('ChoicePicker', () => {
  it('ticks a value as a field=value condition', async () => {
    const wrapper = pick(oracle)
    expect(wrapper.findAll('h3').map((h) => h.text())).toEqual(['region', 'tags'])
    expect(wrapper.text()).toContain('Nothing ticked.')

    await box(wrapper, 'us-ashburn-1').setValue(true)
    await box(wrapper, 'OCI').setValue(true)
    expect(wrapper.props('modelValue')).toEqual(['region=us-ashburn-1', 'tags=OCI'])
    expect(wrapper.text()).toContain('2 ticked: region=us-ashburn-1, tags=OCI')

    await box(wrapper, 'us-ashburn-1').setValue(false)
    expect(wrapper.props('modelValue')).toEqual(['tags=OCI'])
  })

  it('ticks a key as the bare name', async () => {
    const wrapper = pick(github)
    expect(wrapper.get('h3').text()).toBe('Listed under')
    await box(wrapper, 'hooks').setValue(true)
    expect(wrapper.props('modelValue')).toEqual(['hooks'])
  })

  // Somebody after every US region finds them and takes them all.
  it('takes all of what the search found in a group', async () => {
    const wrapper = pick(oracle)
    await wrapper.get('input[type=search]').setValue('us-')
    const values = wrapper.findAll('label').filter((l) => l.find('input[type=checkbox]').exists())
    expect(values.map((l) => l.text())).toEqual(['us-ashburn-1', 'us-phoenix-1'])
    await wrapper.get('button.link').trigger('click')
    expect(wrapper.props('modelValue')).toEqual(['region=us-ashburn-1', 'region=us-phoenix-1'])
  })

  it('keeps the order the list offers, whatever order the ticks came in', async () => {
    const wrapper = pick(oracle)
    await box(wrapper, 'OCI').setValue(true)
    await box(wrapper, 'us-phoenix-1').setValue(true)
    await box(wrapper, 'eu-frankfurt-1').setValue(true)
    expect(wrapper.props('modelValue')).toEqual([
      'region=eu-frankfurt-1',
      'region=us-phoenix-1',
      'tags=OCI',
    ])
  })

  // The router ignores case, so a condition written in another case is
  // the same tick, not a second one.
  it('reads a condition in any case as its tick', () => {
    const wrapper = pick(oracle, ['TAGS=oci'])
    expect(box(wrapper, 'OCI').element.checked).toBe(true)
    expect(wrapper.text()).not.toContain('Also kept')
  })

  // A pattern, or a value the list has since dropped, is shown and can be
  // removed on its own, never dropped because the list does not offer it.
  it('shows what the list does not offer, and removes it on request', async () => {
    const wrapper = pick(oracle, ['region=us-*', 'tags=OCI', 'region=gone-1'])
    expect(wrapper.text()).toContain('Also kept, and not in this list:')
    expect(wrapper.text()).toContain('region=us-*')
    expect(wrapper.text()).toContain('region=gone-1')

    await box(wrapper, 'OSN').setValue(true)
    expect(wrapper.props('modelValue')).toEqual([
      'tags=OCI',
      'tags=OSN',
      'region=us-*',
      'region=gone-1',
    ])
    await wrapper.get('button[aria-label="Stop keeping region=gone-1"]').trigger('click')
    expect(wrapper.props('modelValue')).toEqual(['tags=OCI', 'tags=OSN', 'region=us-*'])
  })

  it('clears every tick at once', async () => {
    const wrapper = pick(oracle, ['tags=OCI', 'region=us-*'])
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Clear 2')
      .trigger('click')
    expect(wrapper.props('modelValue')).toEqual([])
  })
})
