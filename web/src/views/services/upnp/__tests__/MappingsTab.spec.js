import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import MappingsTab from '@/views/services/upnp/MappingsTab.vue'

vi.mock('@/lib/api', () => ({
  api: { upnp: { mappings: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

// Live reads from the start, so no tab outlives its test.
enableAutoUnmount(afterEach)

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

  afterEach(() => vi.useRealTimers())

  it('searches what a row shows', async () => {
    api.upnp.mappings.mockResolvedValue([
      { protocol: 'udp', externalPort: 3074, internal: '10.0.0.20', internalPort: 3074 },
      { protocol: 'tcp', externalPort: 32400, internal: '10.0.0.3', internalPort: 32400 },
    ])
    const wrapper = mount(MappingsTab)
    await flushPromises()
    await wrapper.get('input[type=search]').setValue('32400')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('1 of 2')
    await wrapper.get('input[type=search]').setValue('10.0.0.99')
    expect(wrapper.text()).toContain('Nothing matches "10.0.0.99".')
  })

  // Clients open and close their mappings on their own, so Live reads the
  // list again every few seconds, from the start.
  it('reads again while Live is on', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    api.upnp.mappings.mockResolvedValue([])
    const wrapper = mount(MappingsTab)
    await flushPromises()
    const live = wrapper.findAll('button').find((b) => b.text() === 'Live')
    expect(live.attributes('aria-pressed')).toBe('true')
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(api.upnp.mappings).toHaveBeenCalledTimes(2)
    await live.trigger('click')
    vi.advanceTimersByTime(6000)
    expect(api.upnp.mappings).toHaveBeenCalledTimes(2)
  })
})
