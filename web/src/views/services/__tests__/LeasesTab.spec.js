import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useToastStore } from '@/stores/toast'
import LeasesTab from '@/views/services/dhcp/LeasesTab.vue'

vi.mock('@/lib/api', () => ({
  api: { services: { leases: vi.fn() }, wol: { wake: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  AppDialog: { props: ['open', 'title'], template: '<div v-if="open"><slot /></div>' },
}

const printer = {
  ip: '192.168.1.50',
  mac: 'AA:BB:CC:DD:EE:01',
  hostname: 'printer',
  expires: '2026-09-24T12:00:00Z',
  family: 4,
}

async function tab(leases, { role = 'admin' } = {}) {
  api.services.leases.mockResolvedValue(leases)
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  config.replaceDraft({
    version: 6,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true },
      { name: 'eth1', zone: 'lan', enabled: true },
    ],
    services: { dhcp: { enabled: true } },
  })
  config.markSaved()
  const wrapper = mount(LeasesTab, { global: { stubs } })
  await flushPromises()
  return { wrapper, config }
}

const makeStatic = (w) => w.findAll('button').filter((b) => b.text() === 'Make static')

describe('LeasesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('pins a client to the address it has', async () => {
    const { wrapper, config } = await tab([printer])
    await makeStatic(wrapper)[0].trigger('click')
    expect(wrapper.get('#sl-mac').element.value).toBe('aa:bb:cc:dd:ee:01')
    expect(wrapper.get('#sl-ip').element.value).toBe('192.168.1.50')

    await wrapper.get('form').trigger('submit')
    expect(config.draft.services.dhcp.staticLeases).toEqual([
      { mac: 'aa:bb:cc:dd:ee:01', ip: '192.168.1.50', hostname: 'printer' },
    ])
  })

  // A static lease has nothing to pin, and a DHCPv6 client has no MAC to
  // pin it by.
  it('offers it only where it means something', async () => {
    const { wrapper } = await tab([
      printer,
      { ...printer, mac: 'aa:bb:cc:dd:ee:02', ip: '192.168.1.2', static: true },
      { ip: 'fd00::5', clientId: '00:01:00:01', family: 6, expires: '2026-09-24T12:00:00Z' },
    ])
    expect(makeStatic(wrapper)).toHaveLength(1)
  })

  // The server names the interface a lease came from; a wake goes out
  // there, and only on an inside interface with Ethernet under it.
  it('wakes a client where it got its lease', async () => {
    const { wrapper } = await tab([
      { ...printer, interface: 'eth1' },
      { ...printer, mac: 'aa:bb:cc:dd:ee:02', ip: '203.0.113.9', interface: 'eth0' },
      { ...printer, mac: 'aa:bb:cc:dd:ee:03', ip: '10.9.0.3' },
      { ip: 'fd00::5', clientId: '00:01:00:01', family: 6, expires: '2026-09-24T12:00:00Z' },
    ])
    const wake = wrapper.findAll('button').filter((b) => b.text() === 'Wake')
    expect(wake).toHaveLength(1)
    await wake[0].trigger('click')
    await flushPromises()
    expect(api.wol.wake).toHaveBeenCalledWith({ interface: 'eth1', mac: 'AA:BB:CC:DD:EE:01' })
    expect(useToastStore().toasts.at(-1).message).toBe('Sent a wake packet to printer.')
  })

  it('offers a viewer no wake', async () => {
    const { wrapper } = await tab([{ ...printer, interface: 'eth1' }], { role: 'viewer' })
    expect(wrapper.findAll('button').filter((b) => b.text() === 'Wake')).toHaveLength(0)
  })
})
