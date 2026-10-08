import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SeriesChart from '@/components/SeriesChart.vue'
import { niceScale } from '@/lib/chart'
import { formatMs } from '@/lib/format'

const END = 1_790_000_000
/** A day of minutes: [t, mean, low, high, loss], one unanswered, one missing. */
const points = [
  [END - 240, 3, 2, 5, 0],
  [END - 180, 4, 3, 6, 10],
  [END - 120, null, null, null, 100],
  [END, 6, 6, 6, 0],
]

function chart(props = {}) {
  return mount(SeriesChart, {
    props: {
      label: 'gw_eth0',
      points,
      end: END,
      window: '24h',
      series: [{ index: 1, name: 'IPv4', color: 'var(--chart-down)', band: [2, 3] }],
      bars: [{ index: 4, name: 'IPv4 lost', color: 'var(--chart-down)' }],
      format: formatMs,
      scale: (h) => niceScale(h, 10),
      gap: 90,
      step: 60,
      empty: 'No probes yet.',
      ...props,
    },
  })
}

describe('SeriesChart', () => {
  it('breaks the line where nothing answered and where minutes are missing', () => {
    const w = chart()
    const line = w.findAll('path').find((p) => p.attributes('stroke') === 'var(--chart-down)')
    expect(line.attributes('d').match(/M/g)).toHaveLength(2)
    expect(line.attributes('d').match(/L/g)).toHaveLength(1)
    const band = w.get('path[data-band]')
    expect(band.attributes('d').match(/Z/g)).toHaveLength(2)
  })

  it('draws a bar for each share lost, on its own axis', () => {
    const w = chart()
    const bars = w.findAll('rect[data-bar]')
    expect(bars).toHaveLength(2)
    expect(Number(bars[1].attributes('height'))).toBeGreaterThan(
      Number(bars[0].attributes('height')),
    )
    const labels = w.findAll('text').map((t) => t.text())
    expect(labels).toEqual(expect.arrayContaining(['0%', '50%', '100%', '0.0 ms', '10.0 ms']))
  })

  it('keys the line and the bars with their values now', () => {
    const legend = chart().text()
    expect(legend).toContain('IPv4 6.0 ms')
    expect(legend).toContain('IPv4 lost 0%')
  })

  it('reads a point with its range and its share lost', async () => {
    const w = chart()
    const plot = w.get('[tabindex="0"]')
    await plot.trigger('keydown', { key: 'Home' })
    await plot.trigger('keydown', { key: 'ArrowRight' })
    const readout = w.get('[aria-live="polite"]').text()
    expect(readout).toContain('4.0 msIPv4')
    expect(readout).toContain('3.0 ms to 6.0 ms')
    expect(readout).toContain('10%IPv4 lost')
    await plot.trigger('keydown', { key: 'ArrowRight' })
    expect(w.get('[aria-live="polite"]').text()).toContain('—IPv4')
  })

  it('shows the points as rows, a range under each value', async () => {
    const w = chart()
    await w.get('button').trigger('click')
    const rows = w.findAll('tbody tr').map((r) => r.findAll('td').map((c) => c.text()))
    expect(rows[1].slice(1)).toEqual(['4.0 ms 3.0 ms to 6.0 ms', '10%'])
    expect(rows[2].slice(1)).toEqual(['—', '100%'])
    expect(w.get('caption').text()).toBe('gw_eth0, ipv4 and ipv4 lost')
  })

  it('says what an empty chart means', () => {
    const w = chart({ points: [] })
    expect(w.text()).toContain('No probes yet.')
    expect(w.get('svg[role="img"]').attributes('aria-label')).toBe('gw_eth0: no probes yet')
  })
})
