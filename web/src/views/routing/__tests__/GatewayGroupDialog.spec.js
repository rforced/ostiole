import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import GatewayGroupDialog from '@/views/routing/GatewayGroupDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

function setup(extra) {
  const config = useConfigStore()
  const line = (name) => ({
    name,
    zone: 'wan',
    enabled: true,
    ipv4: { mode: 'dhcp' },
    ipv6: { mode: 'none' },
  })
  const cfg = {
    version: 12,
    zones: [{ name: 'wan', external: true }],
    interfaces: [line('eth0'), line('wwan0')],
    rules: [],
    gateways: [
      { name: 'wan', enabled: true, interface: 'eth0' },
      { name: 'lte', enabled: true, interface: 'wwan0', priority: 1 },
    ],
    gatewayGroups: [
      {
        name: 'failover',
        enabled: true,
        members: [{ gateway: 'wan' }, { gateway: 'lte', tier: 1 }],
        ...extra,
      },
    ],
  }
  config.saved = cfg
  config.replaceDraft(cfg)
  const wrapper = mount(GatewayGroupDialog, {
    props: { open: true, group: config.gatewayGroups[0] },
    global: { stubs },
  })
  return { config, wrapper }
}

describe('GatewayGroupDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it.each([
    ['fallback', { onDown: 'fallback' }, 'fallback'],
    ['block', { onDown: 'block' }, 'block'],
    ['omitted', {}, 'fallback'],
    ['empty', { onDown: '' }, 'fallback'],
  ])(
    'reopens a group with onDown %s and leaves an untouched save alone',
    async (_, extra, shown) => {
      const { config, wrapper } = setup(extra)
      expect(wrapper.get('#gg-ondown').element.value).toBe(shown)
      await wrapper.get('form').trigger('submit')
      expect(config.dirty).toBe(false)
    },
  )

  it('writes onDown when the user picks one', async () => {
    const { config, wrapper } = setup({})
    await wrapper.get('#gg-ondown').setValue('block')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(true)
    expect(config.gatewayGroups[0].onDown).toBe('block')
  })
})
