import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import LeasesTab from '@/views/services/dhcp/LeasesTab.vue'

vi.mock('@/lib/api', () => ({
  api: { services: { leases: vi.fn() } },
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

async function tab(leases) {
  api.services.leases.mockResolvedValue(leases)
  const config = useConfigStore()
  config.replaceDraft({ version: 6, services: { dhcp: { enabled: true } } })
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
})
