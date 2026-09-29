import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import BusyHostsDialog from '@/views/firewall/shaping/BusyHostsDialog.vue'

const stubs = {
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
}

function draft() {
  return {
    version: 3,
    zones: [
      { name: 'lan' },
      { name: 'guest' },
      { name: 'wan', external: true },
      { name: 'dmz', busy: { connections: 50, priority: 'high' } },
    ],
    interfaces: [],
    rules: [],
  }
}

function open(zone = null) {
  const config = useConfigStore()
  config.replaceDraft(draft())
  const wrapper = mount(BusyHostsDialog, { props: { open: true, zone }, global: { stubs } })
  return { wrapper, config }
}

describe('BusyHostsDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('stores the count and the tier on the zone', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#busy-zone').setValue('lan')
    await wrapper.get('#busy-count').setValue('500')
    await wrapper.get('#busy-priority').setValue('bulk')
    await wrapper.get('form').trigger('submit')
    expect(config.busyZones[0].busy).toEqual({ connections: 500, priority: 'bulk' })
  })

  // The tally is per source address, so a zone facing the internet would be
  // counting the whole of it. A zone that already has one is edited.
  it('offers only the zones a tally means anything on', () => {
    const { wrapper } = open()
    const names = wrapper
      .get('#busy-zone')
      .findAll('option')
      .map((o) => o.element.value)
      .filter(Boolean)
    expect(names).toEqual(['lan', 'guest'])
  })

  it('reads an existing limit back and pins its zone', () => {
    const { wrapper } = open({ name: 'dmz', busy: { connections: 50, priority: 'high' } })
    expect(wrapper.get('#busy-count').element.value).toBe('50')
    expect(wrapper.get('#busy-priority').element.value).toBe('high')
    expect(wrapper.get('#busy-zone').attributes('disabled')).toBeDefined()
  })

  // Below the floor an ordinary page load would trip it, which would make
  // the whole network feel broken for no reason anybody could see.
  it('refuses a count the router would not accept', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#busy-zone').setValue('lan')
    await wrapper.get('#busy-count').setValue('2')
    expect(wrapper.get('button[type=submit]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(config.busyZones.map((z) => z.name)).toEqual(['dmz'])
  })
})
