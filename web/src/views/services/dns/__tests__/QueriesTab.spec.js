import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import QueriesTab from '@/views/services/dns/QueriesTab.vue'

vi.mock('@/lib/api', () => ({
  api: {
    queries: { list: vi.fn(), summary: vi.fn(), clear: vi.fn() },
    blocking: { status: vi.fn(), lookup: vi.fn() },
  },
  ApiError: class ApiError extends Error {},
}))

/** A log of 450 answers, newest first, read 200 at a time like the server. */
function answers({ before } = {}) {
  const all = Array.from({ length: 450 }, (_, i) => ({
    seq: 450 - i,
    time: '2026-09-24T12:00:00Z',
    client: '10.0.0.2',
    name: `name${450 - i}.example`,
    type: 'A',
    status: 'ok',
  }))
  const from = before ? all.findIndex((e) => e.seq < before) : 0
  return { enabled: true, total: all.length, entries: all.slice(from, from + 200) }
}

/** Vue Test Utils stubs TransitionGroup, so the rows sit in no tbody. */
function names(wrapper) {
  return wrapper
    .findAll('tr')
    .map((tr) => tr.findAll('td')[2]?.text())
    .filter(Boolean)
}

function button(wrapper, label) {
  return wrapper.findAll('button').find((b) => b.text() === label)
}

describe('QueriesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.queries.list.mockImplementation(async (params) => answers(params))
    api.queries.summary.mockResolvedValue({ total: 450, blocked: 0, clients: 1 })
    api.blocking.status.mockResolvedValue({ lists: [] })
  })

  // Reading back through a big log used to keep every page it had read,
  // so a million answers ended up in the browser. One page is kept.
  it('pages through the log a page at a time', async () => {
    const config = useConfigStore()
    config.draft = { version: 6, services: { dns: { enabled: true, queryLog: { enabled: true } } } }
    config.loaded = true
    const wrapper = mount(QueriesTab)
    await flushPromises()

    expect(names(wrapper)).toHaveLength(200)
    expect(names(wrapper)[0]).toBe('name450.example')
    expect(wrapper.text()).toContain('1–200 of 450.')
    expect(button(wrapper, 'Newer')).toBeUndefined()

    await button(wrapper, 'Older').trigger('click')
    await flushPromises()
    expect(api.queries.list).toHaveBeenLastCalledWith(expect.objectContaining({ before: 251 }))
    expect(names(wrapper)).toHaveLength(200)
    expect(names(wrapper)[0]).toBe('name250.example')
    expect(wrapper.text()).toContain('201–400 of 450.')

    await button(wrapper, 'Older').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toHaveLength(50)
    expect(wrapper.text()).toContain('401–450 of 450.')
    expect(button(wrapper, 'Older')).toBeUndefined()

    await button(wrapper, 'Newer').trigger('click')
    await flushPromises()
    expect(names(wrapper)[0]).toBe('name250.example')
    await button(wrapper, 'Newer').trigger('click')
    await flushPromises()
    expect(names(wrapper)[0]).toBe('name450.example')
    expect(button(wrapper, 'Newer')).toBeUndefined()
  })
})
