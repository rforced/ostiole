import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import ServerDialog from '@/views/services/dhcp/ServerDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

const pool = {
  interface: 'eth1',
  enabled: true,
  rangeStart: '192.0.2.100',
  rangeEnd: '192.0.2.199',
}

const options = {
  searchDomains: ['lab.example', 'corp.example'],
  ntp: ['192.0.2.10', '192.0.2.11'],
  bootServer: '192.0.2.5',
  bootFile: 'pxelinux.0',
}

/** The pool as saved, and its dialog open on it. */
function openPool(server) {
  const config = useConfigStore()
  const cfg = {
    version: 12,
    zones: [{ name: 'lan' }],
    interfaces: [
      {
        name: 'eth1',
        zone: 'lan',
        enabled: true,
        ipv4: { mode: 'static', address: '192.0.2.1/24' },
        ipv6: { mode: 'none' },
      },
    ],
    rules: [],
    services: { dhcp: { enabled: true, servers: [server] }, dns: { enabled: false } },
  }
  config.saved = cfg
  config.replaceDraft(cfg)
  const wrapper = mount(ServerDialog, {
    props: { open: true, server: config.draft.services.dhcp.servers[0] },
    global: { stubs },
  })
  return { config, wrapper }
}

describe('DHCP ServerDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens a pool as saved and leaves an untouched save alone', async () => {
    const { config, wrapper } = openPool(pool)
    expect(wrapper.get('#sc-lease').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('reopens the options as saved and leaves an untouched save alone', async () => {
    const { config, wrapper } = openPool({ ...pool, ...options })
    expect(wrapper.get('#sc-search').element.value).toBe('lab.example, corp.example')
    expect(wrapper.get('#sc-time').element.value).toBe('192.0.2.10, 192.0.2.11')
    expect(wrapper.get('#sc-boot-server').element.value).toBe('192.0.2.5')
    expect(wrapper.get('#sc-boot-file').element.value).toBe('pxelinux.0')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('writes the search domains and time servers as lists and the boot pair as text', async () => {
    const { config, wrapper } = openPool(pool)
    await wrapper.get('#sc-search').setValue('lab.example, corp.example')
    await wrapper.get('#sc-time').setValue('192.0.2.10, 192.0.2.11')
    await wrapper.get('#sc-boot-server').setValue('192.0.2.5')
    await wrapper.get('#sc-boot-file').setValue('pxelinux.0')
    await wrapper.get('form').trigger('submit')
    expect(config.draft.services.dhcp.servers[0]).toStrictEqual({ ...pool, ...options })
  })

  it('drops the options when they are cleared', async () => {
    const { config, wrapper } = openPool({ ...pool, ...options })
    for (const id of ['#sc-search', '#sc-time', '#sc-boot-server', '#sc-boot-file']) {
      await wrapper.get(id).setValue('')
    }
    await wrapper.get('form').trigger('submit')
    expect(config.draft.services.dhcp.servers[0]).toStrictEqual(pool)
  })
})
