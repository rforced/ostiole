import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import AliasDialog from '@/views/firewall/AliasDialog.vue'

const stubs = {
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
  CountryPicker: true,
}

function open(alias = null, feeds = []) {
  const config = useConfigStore()
  config.replaceDraft({
    version: 6,
    zones: [],
    interfaces: [],
    aliases: alias ? [alias] : [],
    rules: [],
    nat: { outbound: { mode: 'automatic' } },
  })
  const wrapper = mount(AliasDialog, { props: { open: true, alias, feeds }, global: { stubs } })
  return { wrapper, config }
}

describe('AliasDialog AS numbers', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // The numbers are keys the router expands, so there is no URL to give,
  // and the schedule that refetches them is shown instead.
  it('saves the numbers in one spelling, without repeats', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#alias-name').setValue('google')
    await wrapper.get('#alias-type').setValue('asn')
    expect(wrapper.find('#alias-url').exists()).toBe(false)
    expect(wrapper.find('#alias-refresh').exists()).toBe(true)
    await wrapper.get('#alias-entries').setValue('as15169\n15169, AS36040')
    await wrapper.get('form').trigger('submit')
    expect(config.aliases).toEqual([
      { name: 'google', type: 'asn', entries: ['AS15169', 'AS36040'], refreshHours: 24 },
    ])
  })

  it('refuses what is not an AS number, and an empty list', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#alias-name').setValue('google')
    await wrapper.get('#alias-type').setValue('asn')
    await wrapper.get('#alias-entries').setValue('AS15169\ngoogle')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role=alert]').text()).toContain('google is not an AS number')

    await wrapper.get('#alias-entries').setValue('')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role=alert]').text()).toContain('at least one AS number')
    expect(config.aliases).toEqual([])
  })

  it('shows what each number held at the last fetch', () => {
    const alias = { name: 'google', type: 'asn', entries: ['AS15169', 'AS64512'] }
    const feeds = [
      {
        alias: 'google',
        parts: [
          { source: 'x', asn: 'AS15169', holder: 'GOOGLE - Google LLC', entries: 1415 },
          { source: 'y', asn: 'AS64512', entries: 0 },
        ],
      },
    ]
    const { wrapper } = open(alias, feeds)
    expect(wrapper.get('#alias-entries').element.value).toBe('AS15169\nAS64512')
    const text = wrapper.text()
    expect(text).toContain('AS15169 · GOOGLE - Google LLC · 1,415 prefixes')
    expect(text).toContain('AS64512 · 0 prefixes')
  })
})
