import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import LogTab from '@/views/wireless/LogTab.vue'

vi.mock('@/lib/api', () => ({
  api: { wireless: { log: vi.fn() }, systemStats: vi.fn(), logFiles: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

class FakeSource {
  close() {}
}

async function tab(list, { level = 'info', radio = true } = {}) {
  api.wireless.log.mockResolvedValue({ entries: list, held: list.length, kept: level === 'info' })
  api.systemStats.mockResolvedValue({ memTotal: 8_000_000_000 })
  api.logFiles.mockResolvedValue({ enabled: false, logs: [] })
  const store = useConfigStore()
  const draft = { version: 11, system: { logging: { level } } }
  if (radio) draft.wireless = { country: 'US', radios: [{ name: 'wlp3s0', enabled: true }] }
  store.draft = draft
  store.saved = structuredClone(draft)
  store.loaded = true
  const wrapper = mount(LogTab)
  await flushPromises()
  return { wrapper, store }
}

describe('Wireless log', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('shows clients joining and leaving', async () => {
    const { wrapper } = await tab([
      {
        seq: 2,
        time: '2026-09-28T14:02:01Z',
        event: 'wrong password',
        interface: 'ap1',
        mac: '12:34:56:78:9a:bd',
      },
      {
        seq: 1,
        time: '2026-09-28T14:02:00Z',
        event: 'connected',
        interface: 'ap0',
        mac: '12:34:56:78:9a:bc',
        network: 'Home',
        device: 'phone',
      },
    ])
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].find('.badge').classes()).toContain('badge-bad')
    expect(rows[0].text()).toContain('ap1')
    expect(rows[1].text()).toContain('Home')
    expect(rows[1].text()).toContain('phone')
    expect(rows[1].text()).toContain('12:34:56:78:9a:bc')
  })

  it('says which log levels keep it, and when no radio is on', async () => {
    let { wrapper } = await tab([], { level: 'warning' })
    expect(wrapper.text()).toContain('The wireless log is kept at the Info and Debug log levels')
    ;({ wrapper } = await tab([], { radio: false }))
    expect(wrapper.text()).toContain('No radio is on, so no clients arrive.')
  })

  // Nothing is written until a figure is set, and the block goes when the
  // last one does.
  it('keeps the wireless block out of the draft while it is empty', async () => {
    const { wrapper, store } = await tab([], { radio: false })
    expect(store.draft.wireless).toBeUndefined()
    await wrapper.find('#wireless-days').setValue('3')
    expect(store.draft.wireless).toEqual({ log: { days: 3 } })
    await wrapper.find('#wireless-days').setValue('')
    expect(store.draft.wireless).toBeUndefined()
  })
})
