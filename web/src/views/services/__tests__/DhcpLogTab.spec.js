import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import LogTab from '@/views/services/dhcp/LogTab.vue'

vi.mock('@/lib/api', () => ({
  api: { services: { dhcpLog: vi.fn() }, systemStats: vi.fn(), logFiles: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

class FakeSource {
  close() {}
}

async function tab(list, { level = 'info', dhcp = true } = {}) {
  api.services.dhcpLog.mockResolvedValue({
    entries: list,
    held: list.length,
    kept: level === 'info',
  })
  api.systemStats.mockResolvedValue({ memTotal: 8_000_000_000 })
  api.logFiles.mockResolvedValue({ enabled: false, logs: [] })
  const store = useConfigStore()
  const draft = {
    version: 11,
    system: { logging: { level } },
    services: { dhcp: { enabled: dhcp }, dns: { enabled: false } },
  }
  store.draft = draft
  store.saved = structuredClone(draft)
  store.loaded = true
  const wrapper = mount(LogTab)
  await flushPromises()
  return { wrapper, store }
}

describe('DHCP log', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('shows what the server said of each client', async () => {
    const { wrapper } = await tab([
      {
        seq: 2,
        time: '2026-09-28T14:02:01Z',
        message: 'NAK',
        interface: 'eth1',
        address: '10.0.0.9',
        mac: 'aa:bb:cc:dd:ee:ff',
        detail: 'wrong network',
      },
      {
        seq: 1,
        time: '2026-09-28T14:02:00Z',
        message: 'ACK',
        interface: 'eth1',
        address: '10.0.0.50',
        mac: 'aa:bb:cc:dd:ee:ff',
        name: 'switch',
        device: 'switch',
      },
    ])
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].find('.badge').classes()).toContain('badge-bad')
    expect(rows[0].text()).toContain('wrong network')
    expect(rows[1].text()).toContain('ACK')
    expect(rows[1].text()).toContain('10.0.0.50')
    expect(rows[1].text()).toContain('aa:bb:cc:dd:ee:ff')
    expect(api.services.dhcpLog).toHaveBeenCalledWith({ limit: 200 }, expect.any(AbortSignal))
  })

  it('says which log levels keep it, and when DHCP is off', async () => {
    let { wrapper } = await tab([], { level: 'warning' })
    expect(wrapper.text()).toContain('The DHCP log is kept at the Info and Debug log levels')
    ;({ wrapper } = await tab([], { dhcp: false }))
    expect(wrapper.text()).toContain('DHCP is off, so no new messages arrive.')
    expect(wrapper.text()).toContain('No messages.')
  })

  it('keeps how many messages and days in the draft', async () => {
    const { wrapper, store } = await tab([])
    await wrapper.find('#dhcp-entries').setValue('5000')
    expect(store.draft.services.dhcp.log).toEqual({ entries: 5000 })
    await wrapper.find('#dhcp-entries').setValue('')
    expect(store.draft.services.dhcp.log).toBeUndefined()
  })
})
