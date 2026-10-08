import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import GatewayStrip from '@/views/dashboard/GatewayStrip.vue'

const START = 1_790_000_000
function strip(kinds, over = {}) {
  const cells = Array.from({ length: 144 }, (_, i) => ({ start: START + i * 600 }))
  kinds.forEach((c, i) => Object.assign(cells[i], c))
  return { cells, down: 0, worstLatencyMs: 0, worstLossPercent: 0, families: 1, ...over }
}

describe('GatewayStrip', () => {
  // Down is the tallest whatever the colour; slow and lossy grow with how
  // far past their threshold they went; up is a low bar; nothing answered,
  // none.
  it('draws how bad each cell was as its height', () => {
    const w = mount(GatewayStrip, {
      props: {
        label: 'gw_eth0',
        strip: strip([
          { kind: 'up' },
          { kind: 'slow', level: 0 },
          { kind: 'lossy', level: 1 },
          { kind: 'down' },
          { kind: 'never' },
        ]),
      },
    })
    const bars = w.findAll('rect[data-kind]')
    expect(bars.map((b) => [b.attributes('data-kind'), Number(b.attributes('height'))])).toEqual([
      ['up', 6],
      ['slow', 12],
      ['lossy', 18],
      ['down', 24],
    ])
    expect(bars.map((b) => b.attributes('fill'))).toEqual([
      'var(--ok-fill)',
      'var(--warn-fill)',
      'var(--warn-fill)',
      'var(--bad-fill)',
    ])
    expect(w.findAll('rect')).toHaveLength(144 + 4)
  })

  it("titles each cell with its figures, and says the day's", () => {
    const w = mount(GatewayStrip, {
      props: {
        label: 'gw_eth0',
        strip: strip([{ kind: 'down', down: 4, lossPercent: 100 }], {
          down: 240,
          worstLatencyMs: 310,
          worstLatencyFamily: 'IPv6',
          worstLossPercent: 100,
          worstLossFamily: 'IPv4',
          families: 2,
        }),
      },
    })
    expect(w.find('rect[data-kind] title').text()).toMatch(/: down, down 4m, 100% lost$/)
    expect(w.get('p').text()).toBe(
      'Down 4m · worst 310 ms (IPv6) · worst 100% lost (IPv4) · to both next hops',
    )
    expect(w.get('svg').attributes('aria-label')).toContain('gw_eth0, the last 24 hours: Down 4m')
  })

  it('says a day without probes, and greys while reading', () => {
    expect(
      mount(GatewayStrip, { props: { label: 'lte', strip: strip([]) } })
        .get('p')
        .text(),
    ).toBe('No probes in 24 hours.')
    const w = mount(GatewayStrip, { props: { label: 'lte', skeleton: true } })
    expect(w.find('.skeleton').exists()).toBe(true)
    expect(w.findAll('rect')).toHaveLength(1)
  })
})
