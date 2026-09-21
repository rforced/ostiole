import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import ConnectionsPage from '@/views/diagnostics/ConnectionsPage.vue'

vi.mock('@/lib/api', () => ({
  api: { diagnostics: { states: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = { RouterLink: true }

/** A table with `total` of `max` entries in it and nothing else to say. */
function table(total, max) {
  return { states: [], total, matched: total, byProtocol: { tcp: total }, max }
}

async function page(result) {
  api.diagnostics.states.mockResolvedValue(result)
  const wrapper = mount(ConnectionsPage, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('ConnectionsPage', () => {
  beforeEach(() => vi.clearAllMocks())

  // A full table drops packets and says so only in dmesg, so the count is
  // shown against the ceiling rather than on its own.
  it('counts the table against the ceiling the kernel is enforcing', async () => {
    const wrapper = await page(table(65536, 262144))
    const meter = wrapper.find('[role="meter"]')
    expect(meter.attributes('aria-valuenow')).toBe('25')
    expect(wrapper.text()).toContain('65,536 of 262,144')
  })

  // The percentage is readable on its own; the colour only repeats it.
  it('warns before the table is full, not once it already is', async () => {
    const quiet = await page(table(1000, 262144))
    expect(quiet.text()).not.toContain('drops packets')

    const busy = await page(table(210000, 262144))
    expect(busy.find('[role="meter"]').attributes('aria-valuenow')).toBe('80')
    expect(busy.text()).toContain('drops packets')
  })

  // An older kernel, or one with the module unloaded, reports no ceiling.
  // The rows are still the answer; there is just nothing to count against.
  it('leaves the meter out when the kernel will not say what its limit is', async () => {
    const wrapper = await page(table(1000, undefined))
    expect(wrapper.find('[role="meter"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('1000 connection(s) tracked')
  })
})
