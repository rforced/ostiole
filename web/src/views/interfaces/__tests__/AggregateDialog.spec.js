import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import AggregateDialog from '@/views/interfaces/AggregateDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

describe('AggregateDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('says 0 turns the link check off only where the kernel does not check anyway', async () => {
    useConfigStore().replaceDraft({ version: 12, zones: [], interfaces: [], rules: [] })
    const wrapper = mount(AggregateDialog, {
      props: { open: true, kind: 'bond', candidates: [] },
      global: { stubs },
    })
    expect(wrapper.text()).toContain('100 is the default. 0 turns the check off.')
    await wrapper.get('#agg-mode').setValue('802.3ad')
    expect(wrapper.text()).toContain('100 is the default.')
    expect(wrapper.text()).not.toContain('0 turns the check off.')
  })

  it('shows an existing bond as saved and leaves an untouched save alone', async () => {
    const config = useConfigStore()
    const port = { enabled: true, ipv4: { mode: 'none' }, ipv6: { mode: 'none' } }
    config.replaceDraft({
      version: 12,
      zones: [],
      rules: [],
      interfaces: [
        { name: 'eth1', ...port },
        { name: 'eth2', ...port },
        { name: 'bond0', ...port, bond: { members: ['eth1', 'eth2'], mode: 'active-backup' } },
      ],
    })
    const before = JSON.stringify(config.draft)
    const wrapper = mount(AggregateDialog, {
      props: { open: true, kind: 'bond', candidates: [], iface: config.findInterface('bond0') },
      global: { stubs },
    })
    expect(wrapper.get('#agg-mii').element.value).toBe('0')
    await wrapper.get('form').trigger('submit')
    expect(config.findInterface('bond0').bond).toEqual({
      members: ['eth1', 'eth2'],
      mode: 'active-backup',
    })
    expect(JSON.stringify(config.draft)).toBe(before)
  })
})
