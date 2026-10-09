import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import StaticRouteDialog from '@/views/routing/StaticRouteDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

function setup(index) {
  const config = useConfigStore()
  const cfg = {
    version: 12,
    zones: [{ name: 'lan' }],
    interfaces: [{ name: 'eth1', zone: 'lan', enabled: true }],
    rules: [],
    routes: [
      {
        id: 'rt-lab',
        description: 'Lab bench',
        enabled: true,
        destination: '198.51.100.0/24',
        gateway: '192.0.2.10',
        interface: 'eth1',
      },
      { id: 'rt-spare', enabled: false, destination: '203.0.113.0/24', gateway: '192.0.2.20' },
    ],
  }
  config.saved = cfg
  config.replaceDraft(cfg)
  const wrapper = mount(StaticRouteDialog, {
    props: { open: true, route: config.routes[index] },
    global: { stubs },
  })
  return { config, wrapper }
}

describe('StaticRouteDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it.each([
    [0, '198.51.100.0/24', 'eth1'],
    [1, '203.0.113.0/24', ''],
  ])('reopens route %i as saved and leaves an untouched save alone', async (i, dest, iface) => {
    const { config, wrapper } = setup(i)
    expect(wrapper.get('#rt-dest').element.value).toBe(dest)
    expect(wrapper.get('#rt-if').element.value).toBe(iface)
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('writes a changed gateway', async () => {
    const { config, wrapper } = setup(0)
    await wrapper.get('#rt-gw').setValue('192.0.2.11')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(true)
    expect(config.routes.map((r) => r.id)).toEqual(['rt-lab', 'rt-spare'])
    expect(config.routes[0].gateway).toBe('192.0.2.11')
  })
})
