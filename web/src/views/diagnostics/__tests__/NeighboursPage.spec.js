import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import NeighboursPage from '@/views/diagnostics/NeighboursPage.vue'

vi.mock('@/lib/api', () => ({
  api: { diagnostics: { neighbours: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const table = [
  {
    address: '10.0.0.20',
    mac: 'aa:bb:cc:00:00:01',
    interface: 'lan0',
    family: 'IPv4',
    state: 'REACHABLE',
  },
  {
    address: '10.0.0.3',
    mac: 'aa:bb:cc:00:00:02',
    interface: 'lan0',
    family: 'IPv4',
    state: 'STALE',
  },
  {
    address: '192.0.2.1',
    mac: 'aa:bb:cc:00:00:03',
    interface: 'wan0',
    family: 'IPv4',
    state: 'REACHABLE',
  },
]

async function page(rows = table) {
  api.diagnostics.neighbours.mockResolvedValue(rows)
  const config = useConfigStore()
  config.replaceDraft({
    version: 7,
    services: { dhcp: { staticLeases: [{ mac: 'AA:BB:CC:00:00:01', hostname: 'printer' }] } },
  })
  const wrapper = mount(NeighboursPage)
  await flushPromises()
  return wrapper
}

const addresses = (w) =>
  w
    .findAll('tbody tr')
    .map((r) => r.find('td').text())
    .map((t) => t.split(/\s/)[0])

describe('NeighboursPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('opens by interface, then address', async () => {
    const w = await page()
    expect(addresses(w)).toEqual(['10.0.0.3', '10.0.0.20', '192.0.2.1'])
  })

  // A static lease names the MAC, so the name finds it too.
  it('searches addresses, MACs and the names static leases give them', async () => {
    const w = await page()
    const search = w.get('input[type=search]')
    await search.setValue('printer')
    expect(addresses(w)).toEqual(['10.0.0.20'])
    expect(w.text()).toContain('1 of 3')
    await search.setValue('aabbcc000003')
    expect(addresses(w)).toEqual(['192.0.2.1'])
    await search.setValue('nothing')
    expect(w.get('tbody td').text()).toBe('Nothing matches "nothing".')
  })

  it('sorts by a column header', async () => {
    const w = await page()
    const state = w.findAll('th').find((th) => th.text() === 'State')
    await state.get('button').trigger('click')
    expect(state.attributes('aria-sort')).toBe('ascending')
    expect(addresses(w)[2]).toBe('10.0.0.3')
  })

  // A phone's private address, beside the MAC, as on the leases tab.
  it('marks a MAC the device made up', async () => {
    const w = await page([
      {
        address: '10.0.0.7',
        mac: 'da:a1:19:3b:22:10',
        interface: 'lan0',
        family: 'IPv4',
        state: 'STALE',
      },
      {
        address: '10.0.0.8',
        mac: '3c:0a:f3:60:c4:60',
        interface: 'lan0',
        family: 'IPv4',
        state: 'STALE',
      },
    ])
    const macs = w.findAll('tbody tr').map((r) => r.findAll('td')[1].text())
    expect(macs).toEqual(['da:a1:19:3b:22:10 random', '3c:0a:f3:60:c4:60'])
  })
})
