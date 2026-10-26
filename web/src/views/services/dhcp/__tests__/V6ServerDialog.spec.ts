import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { Config } from '@/lib/model.gen'
import { useConfigStore } from '@/stores/config'
import V6ServerDialog from '@/views/services/dhcp/V6ServerDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

const advert = { interface: 'eth1', enabled: true, mode: 'stateless' }

const options = {
  searchDomains: ['lab.example', 'corp.example'],
  ntp: ['2001:db8:1::123', '2001:db8:1::124'],
}

/** The advertisement as saved, and its dialog open on it. */
function openAdvert(server) {
  const config = useConfigStore()
  const cfg = {
    version: 12,
    zones: [{ name: 'lan' }],
    interfaces: [
      {
        name: 'eth1',
        zone: 'lan',
        enabled: true,
        ipv4: { mode: 'none' },
        ipv6: { mode: 'static', address: '2001:db8:1::1/64' },
      },
    ],
    rules: [],
    services: { dhcp: { enabled: true, v6: [server] }, dns: { enabled: false } },
  } as Config
  config.saved = cfg
  config.replaceDraft(cfg)
  const wrapper = mount(V6ServerDialog, {
    props: { open: true, server: config.draft.services.dhcp.v6[0] },
    global: { stubs },
  })
  return { config, wrapper }
}

describe('DHCP V6ServerDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens an advertisement as saved and leaves an untouched save alone', async () => {
    const { config, wrapper } = openAdvert({ interface: 'eth1', enabled: true, mode: 'slaac' })
    expect(wrapper.get<HTMLInputElement>('#v6-lease').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('reopens the options as saved and leaves an untouched save alone', async () => {
    const { config, wrapper } = openAdvert({ ...advert, ...options })
    expect(wrapper.get<HTMLInputElement>('#v6-search').element.value).toBe(
      'lab.example, corp.example',
    )
    expect(wrapper.get<HTMLInputElement>('#v6-time').element.value).toBe(
      '2001:db8:1::123, 2001:db8:1::124',
    )
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('writes the search domains and time servers as lists', async () => {
    const { config, wrapper } = openAdvert(advert)
    await wrapper.get('#v6-search').setValue('lab.example, corp.example')
    await wrapper.get('#v6-time').setValue('2001:db8:1::123, 2001:db8:1::124')
    await wrapper.get('form').trigger('submit')
    expect(config.draft.services.dhcp.v6[0]).toStrictEqual({ ...advert, ...options })
  })

  it('drops the options when they are cleared', async () => {
    const { config, wrapper } = openAdvert({ ...advert, ...options })
    await wrapper.get('#v6-search').setValue('')
    await wrapper.get('#v6-time').setValue('')
    await wrapper.get('form').trigger('submit')
    expect(config.draft.services.dhcp.v6[0]).toStrictEqual(advert)
  })

  // SLAAC sends no DHCPv6, so no time server; the search list still goes out in the RA.
  it('drops the time servers when the mode becomes SLAAC', async () => {
    const { config, wrapper } = openAdvert({ ...advert, ...options })
    await wrapper.get('#v6-mode').setValue('slaac')
    expect(wrapper.find('#v6-time').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(config.draft.services.dhcp.v6[0]).toStrictEqual({
      interface: 'eth1',
      enabled: true,
      mode: 'slaac',
      searchDomains: options.searchDomains,
    })
  })
})
