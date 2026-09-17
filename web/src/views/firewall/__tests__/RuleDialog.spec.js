import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import RuleDialog from '@/views/firewall/RuleDialog.vue'

const stubs = {
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
  EndpointFields: true,
}

function draft(shaped) {
  return {
    version: 3,
    zones: [{ name: 'lan' }, { name: 'wan', external: true }],
    interfaces: [
      {
        name: 'eth0',
        zone: 'wan',
        enabled: true,
        ...(shaped ? { shaping: { download: 1e8 } } : {}),
      },
      { name: 'eth1', zone: 'lan', enabled: true },
    ],
    rules: [],
    nat: { outbound: { mode: 'automatic' } },
  }
}

function open(shaped, rule = null) {
  const config = useConfigStore()
  config.replaceDraft(draft(shaped))
  const wrapper = mount(RuleDialog, {
    props: { open: true, rule, zone: 'lan' },
    global: { stubs },
  })
  return { wrapper, config }
}

describe('RuleDialog priority', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // Asking for a priority on a router that shapes nothing offers a choice
  // with no consequence, so the field is not there at all.
  it('is hidden until something has a line speed', () => {
    expect(open(false).wrapper.find('#rule-priority').exists()).toBe(false)
    expect(open(true).wrapper.find('#rule-priority').exists()).toBe(true)
  })

  it('stores the tier on an accept rule', async () => {
    const { wrapper, config } = open(true)
    await wrapper.get('#rule-priority').setValue('realtime')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].priority).toBe('realtime')
  })

  // A dropped connection has no traffic to prioritise, so the field goes
  // off and takes whatever was chosen with it rather than saving something
  // the router would refuse.
  it('is turned off, and cleared, by a rule that does not accept', async () => {
    const { wrapper, config } = open(true)
    await wrapper.get('#rule-priority').setValue('high')
    await wrapper.get('#rule-action').setValue('drop')
    const select = wrapper.get('#rule-priority')
    expect(select.attributes('disabled')).toBeDefined()
    expect(select.element.value).toBe('')
    expect(wrapper.text()).toContain('Only an accept rule can set a priority.')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].priority).toBeUndefined()
  })

  it('reads an existing tier back', () => {
    const rule = {
      id: 'r1',
      zone: 'lan',
      enabled: true,
      action: 'accept',
      protocol: 'any',
      source: {},
      destination: {},
      priority: 'bulk',
    }
    expect(open(true, rule).wrapper.get('#rule-priority').element.value).toBe('bulk')
  })
})
