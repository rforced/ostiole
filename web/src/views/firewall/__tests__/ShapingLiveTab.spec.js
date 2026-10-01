import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import LiveTab from '@/views/firewall/shaping/LiveTab.vue'

vi.mock('@/lib/api', () => ({
  api: { shaping: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

// Live reads from the start, so no tab outlives its test.
enableAutoUnmount(afterEach)

/** One line's upload, `bytes` sent by `seconds` into the minute. */
function sample(seconds, bytes) {
  return {
    available: true,
    sampledAt: new Date(Date.UTC(2026, 8, 30, 12, 0, seconds)).toISOString(),
    interfaces: [
      {
        name: 'eth0',
        present: true,
        upload: { rate: 20_000_000, installed: true, stats: { bytes, tins: [] } },
      },
    ],
  }
}

describe('Shaping LiveTab', () => {
  beforeEach(() => vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] }))
  afterEach(() => vi.useRealTimers())

  // Off, the queues hold still. Back on, a rate is two new samples apart,
  // never an average over the pause.
  it('takes rates afresh when Live comes back on', async () => {
    api.shaping.mockResolvedValue(sample(0, 0))
    const wrapper = mount(LiveTab)
    await flushPromises()
    const rate = () => wrapper.get('.text-2xl').text()
    const live = wrapper.findAll('button').find((b) => b.text() === 'Live')
    expect(live.attributes('aria-pressed')).toBe('true')
    expect(rate()).toMatch(/^— of 20 Mbit\/s/)

    api.shaping.mockResolvedValue(sample(3, 3_000_000))
    vi.advanceTimersByTime(3000)
    await flushPromises()
    expect(rate()).toMatch(/^8 Mbit\/s/)

    await live.trigger('click')
    vi.advanceTimersByTime(60_000)
    expect(api.shaping).toHaveBeenCalledTimes(2)

    // 80 Mbit/s across the pause, 4 Mbit/s once back on.
    api.shaping.mockResolvedValue(sample(63, 603_000_000))
    await live.trigger('click')
    await flushPromises()
    expect(api.shaping).toHaveBeenCalledTimes(3)
    expect(rate()).toMatch(/^8 Mbit\/s/)
    api.shaping.mockResolvedValue(sample(66, 604_500_000))
    vi.advanceTimersByTime(3000)
    await flushPromises()
    expect(rate()).toMatch(/^4 Mbit\/s/)
  })
})
