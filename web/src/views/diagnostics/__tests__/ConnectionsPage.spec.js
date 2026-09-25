import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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

  describe('order', () => {
    beforeEach(() => vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] }))
    afterEach(() => vi.useRealTimers())

    const row = (source, bytes, ttl) => ({
      protocol: 'tcp',
      source,
      destination: '192.0.2.1',
      bytes,
      packets: 1,
      ttl,
      nat: false,
    })
    const busy = {
      states: [
        row('10.0.0.2', 500, '1h0m0s'),
        row('10.0.0.3', 50, '2m0s'),
        row('10.0.0.1', 10, '30s'),
      ],
      total: 3,
      matched: 3,
      byProtocol: { tcp: 3 },
    }
    const from = (w) => w.findAll('tbody tr').map((r) => r.findAll('td')[1].text())
    const header = (w, name) => w.findAll('th').find((th) => th.text() === name)

    it('sorts by a column, reading how long each has left', async () => {
      const wrapper = await page(busy)
      expect(from(wrapper)).toEqual(['10.0.0.2', '10.0.0.3', '10.0.0.1'])
      await header(wrapper, 'Expires in').get('button').trigger('click')
      expect(from(wrapper)).toEqual(['10.0.0.1', '10.0.0.3', '10.0.0.2'])
    })

    // Live reads every five seconds and keeps the busiest on top.
    it('holds the busiest first while live', async () => {
      const wrapper = await page(busy)
      await header(wrapper, 'From').get('button').trigger('click')
      expect(from(wrapper)).toEqual(['10.0.0.1', '10.0.0.2', '10.0.0.3'])

      const live = wrapper.findAll('button').find((b) => b.text() === 'Live')
      await live.trigger('click')
      await flushPromises()
      expect(api.diagnostics.states).toHaveBeenCalledTimes(2)
      expect(from(wrapper)).toEqual(['10.0.0.2', '10.0.0.3', '10.0.0.1'])
      expect(header(wrapper, 'From').get('button').attributes('disabled')).toBeDefined()
      vi.advanceTimersByTime(5000)
      await flushPromises()
      expect(api.diagnostics.states).toHaveBeenCalledTimes(3)

      await live.trigger('click')
      vi.advanceTimersByTime(10000)
      expect(api.diagnostics.states).toHaveBeenCalledTimes(3)
      expect(header(wrapper, 'Traffic').attributes('aria-sort')).toBe('descending')
    })
  })
})
