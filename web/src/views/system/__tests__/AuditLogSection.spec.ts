import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import type { AuditEvent } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import AuditLogSection from '@/views/system/AuditLogSection.vue'
import { sessionOf, must } from '@/test/fixtures'

vi.mock('@/lib/api', () => ({
  api: { audit: { log: vi.fn(), clear: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

class FakeSource {
  close() {}
}

enableAutoUnmount(afterEach)

const ENTRIES: AuditEvent[] = [
  {
    seq: 3,
    time: '2026-10-09T10:05:00Z',
    action: 'token.create',
    by: { name: 'mira', kind: 'account', role: 'admin', address: '192.0.2.40' },
    text: 'Created the token "deploy".',
  },
  {
    seq: 2,
    time: '2026-10-09T10:01:00Z',
    action: 'config.apply',
    by: { name: 'tobin', kind: 'shell' },
    text: 'Applied revision 12.',
  },
  { seq: 1, time: '2026-10-09T10:00:00Z', action: 'config.revert', by: {}, text: 'Reverted.' },
]

async function card() {
  useAuthStore().user = sessionOf({ username: 'mira', role: 'admin' })
  vi.mocked(api.audit.log).mockResolvedValue({
    entries: ENTRIES,
    more: false,
    held: 3,
    oldest: must(ENTRIES[2]).time,
  })
  const wrapper = mount(AuditLogSection)
  await flushPromises()
  return wrapper
}

describe('AuditLogSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('shows who did each thing, and the sentence', async () => {
    const w = await card()
    const rows = w.findAll('tbody tr').map((r) => r.findAll('td'))
    const cell = (row: number, col: number) => must(rows[row]?.[col])
    expect(cell(0, 1).text()).toContain('mira')
    expect(cell(0, 1).get('div').text()).toBe('admin · 192.0.2.40')
    expect(cell(0, 2).text()).toBe('Created the token "deploy".')
    expect(cell(1, 1).get('div').text()).toBe('shell')
    expect(cell(2, 1).text()).toBe('Router')
    expect(cell(2, 1).find('div').exists()).toBe(false)
    expect(w.text()).toContain('3 entries back to')
  })

  it('clears the log, once asked, and reads it again', async () => {
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const w = await card()
    const reads = vi.mocked(api.audit.log).mock.calls.length
    await must(w.findAll('button').find((b) => b.text() === 'Clear')).trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenLastCalledWith(
      expect.objectContaining({ question: 'Clear the audit log?' }),
    )
    expect(api.audit.clear).toHaveBeenCalledOnce()
    expect(vi.mocked(api.audit.log).mock.calls.length).toBeGreaterThan(reads)
  })
})
