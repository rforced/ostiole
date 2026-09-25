import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import MappingsTab from '@/views/services/upnp/MappingsTab.vue'

vi.mock('@/lib/api', () => ({
  api: { upnp: { mappings: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

describe('MappingsTab', () => {
  it('sorts ports as numbers and clients as addresses', async () => {
    const mapping = (externalPort, internal) => ({
      protocol: 'udp',
      externalPort,
      internal,
      internalPort: externalPort,
    })
    api.upnp.mappings.mockResolvedValue([
      mapping(9000, '10.0.0.20'),
      mapping(443, '10.0.0.100'),
      mapping(51820, '10.0.0.3'),
    ])
    const wrapper = mount(MappingsTab)
    await flushPromises()
    const column = (i) => wrapper.findAll('tbody tr').map((r) => r.findAll('td')[i].text())
    const sortBy = (name) =>
      wrapper
        .findAll('th')
        .find((th) => th.text() === name)
        .get('button')
        .trigger('click')

    await sortBy('External port')
    expect(column(1)).toEqual(['443', '9000', '51820'])
    await sortBy('Client')
    expect(column(2)).toEqual(['10.0.0.3', '10.0.0.20', '10.0.0.100'])
  })
})
