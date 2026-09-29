import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import ClientsTab from '@/views/wireless/ClientsTab.vue'

vi.mock('@/lib/api', () => ({
  api: { wireless: { clients: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

describe('ClientsTab', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // Connected is a time: the client that joined last comes first.
  it('sorts by signal, strongest first, and by who joined last', async () => {
    const client = (mac, signalDbm, connectedSeconds) => ({
      mac,
      signalDbm,
      connectedSeconds,
      rxBytes: 0,
      txBytes: 0,
    })
    api.wireless.clients.mockResolvedValue([
      client('00:11:22:00:00:01', -70, 60),
      client('00:11:22:00:00:02', -40, 7200),
      client('00:11:22:00:00:03', -55, 5),
    ])
    const wrapper = mount(ClientsTab)
    await flushPromises()
    const macs = () => wrapper.findAll('tbody tr').map((r) => r.find('.font-medium').text())
    const sortBy = (name) =>
      wrapper
        .findAll('th')
        .find((th) => th.text() === name)
        .get('button')
        .trigger('click')

    await sortBy('Signal')
    expect(macs()).toEqual(['00:11:22:00:00:02', '00:11:22:00:00:03', '00:11:22:00:00:01'])
    await sortBy('Connected')
    expect(macs()).toEqual(['00:11:22:00:00:03', '00:11:22:00:00:01', '00:11:22:00:00:02'])
  })
})
