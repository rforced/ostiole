import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import ServerDialog from '@/views/services/dhcp/ServerDialog.vue'
import V6ServerDialog from '@/views/services/dhcp/V6ServerDialog.vue'

const stubs = {
  AppDialog: { props: ['open', 'title'], template: '<div><slot /></div>' },
}

function draft() {
  return {
    version: 7,
    zones: [{ name: 'lan' }],
    interfaces: [
      {
        name: 'eth1',
        zone: 'lan',
        enabled: true,
        ipv4: { mode: 'static', address: '10.0.0.1/24' },
        ipv6: { mode: 'static', address: '2001:db8:1::1/64' },
      },
    ],
    rules: [],
    services: {
      dns: { enabled: true, domain: 'lan' },
      dhcp: {
        enabled: true,
        servers: [
          { interface: 'eth1', enabled: true, rangeStart: '10.0.0.100', rangeEnd: '10.0.0.199' },
        ],
        v6: [{ interface: 'eth1', enabled: true, mode: 'stateless' }],
      },
    },
  }
}

async function mountDialog(component, server) {
  const w = mount(component, { props: { open: false, server }, global: { stubs } })
  await w.setProps({ open: true })
  await flushPromises()
  return w
}

function registration(w) {
  return w.findAll('label').find((l) => l.text().includes('DNS registration'))
}

// Off by default: a device's own name is not answered until someone asks
// for it, server by server.
describe('DNS registration on a DHCP server', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    useConfigStore().replaceDraft(draft())
  })

  it('starts off and saves when ticked', async () => {
    const config = useConfigStore()
    const w = await mountDialog(ServerDialog, config.draft.services.dhcp.servers[0])
    const box = registration(w)
    expect(box.text()).toContain('Names devices send resolve under lan.')
    expect(box.find('input').element.checked).toBe(false)
    await box.find('input').setValue(true)
    await w.get('form').trigger('submit')
    expect(config.draft.services.dhcp.servers[0].dnsRegistration).toBe(true)
  })

  it('is left out of an IPv6 server that hands out nothing to name', async () => {
    const config = useConfigStore()
    const w = await mountDialog(V6ServerDialog, config.draft.services.dhcp.v6[0])
    await registration(w).find('input').setValue(true)
    await w.get('#v6-mode').setValue('slaac')
    expect(registration(w)).toBeUndefined()
    await w.get('form').trigger('submit')
    expect(config.draft.services.dhcp.v6[0].dnsRegistration).toBeUndefined()
  })
})
