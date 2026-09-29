import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { nextTick } from 'vue'

import TrafficChart from '@/components/TrafficChart.vue'
import { timeLabel } from '@/lib/chart'

const END = 1_790_000_000
/** A minute of a link at 1 and 2 Mbit/s, then 3 and 4. */
const points = [
  [END - 60, 1_000_000, 2_000_000],
  [END - 30, 1_500_000, 2_500_000],
  [END, 3_000_000, 4_000_000],
]

function chart(props = {}) {
  return mount(TrafficChart, { props: { label: 'eth0', points, end: END, ...props } })
}

describe('TrafficChart', () => {
  it('says it is reading before the first read, and draws nothing', () => {
    const w = chart({ points: null })
    expect(w.text()).toContain('Reading…')
    expect(w.get('svg[role="img"]').attributes('aria-label')).toBe('eth0: reading')
    expect(w.findAll('path').every((p) => !p.attributes('d'))).toBe(true)
  })

  it('keys both lines in the legend with the rate now, and labels the image', () => {
    const w = chart()
    const legend = w.text()
    expect(legend).toContain('Down 3 Mbit/s')
    expect(legend).toContain('Up 4 Mbit/s')
    expect(w.get('svg[role="img"]').attributes('aria-label')).toBe(
      'eth0: down 3 Mbit/s, up 4 Mbit/s',
    )
    const [down, up] = w.findAll('path')
    expect(down.attributes('stroke')).toBe('var(--chart-down)')
    expect(up.attributes('stroke')).toBe('var(--chart-up)')
    expect(down.attributes('d').split('L')).toHaveLength(3)
    // Round rates up the axis, to the busiest point.
    const ticks = w.findAll('text').map((t) => t.text())
    expect(ticks.slice(0, 5)).toEqual(['0 bit/s', '1 Mbit/s', '2 Mbit/s', '3 Mbit/s', '4 Mbit/s'])
  })

  // A window that ends on a whole minute has a stop at the right edge,
  // whose centred label would be cut in half.
  it('leaves out a time whose label would run off the end', () => {
    const end = 1_790_000_040
    const w = chart({ end })
    const labels = w.findAll('text').map((t) => t.text())
    expect(labels).not.toContain(timeLabel(end, '5m'))
    expect(labels).toContain(timeLabel(end - 60, '5m'))
  })

  // The keyboard reads the points as the pointer does: values first.
  it('reads a point with the arrow keys', async () => {
    const w = chart()
    const plot = w.get('[tabindex="0"]')
    // The first key lands on the newest point, the next moves back.
    await plot.trigger('keydown', { key: 'ArrowLeft' })
    expect(w.get('[aria-live="polite"]').text()).toContain('4 Mbit/sUp')
    await plot.trigger('keydown', { key: 'ArrowLeft' })
    const readout = w.get('[aria-live="polite"]')
    expect(readout.text()).toContain('2.5 Mbit/s')
    expect(readout.text()).toContain('1.5 Mbit/s')
    await plot.trigger('keydown', { key: 'Home' })
    expect(w.get('[aria-live="polite"]').text()).toContain('2 Mbit/s')
    await plot.trigger('keydown', { key: 'Escape' })
    expect(w.find('[aria-live="polite"]').exists()).toBe(false)
  })

  it('snaps the crosshair to the point nearest the pointer', async () => {
    const w = chart()
    const svg = w.get('svg[role="img"]')
    svg.element.getBoundingClientRect = () => ({ left: 0, top: 0, width: 640, height: 168 })
    // The right edge of the plot is the newest point.
    svg.element.dispatchEvent(new MouseEvent('pointermove', { clientX: 630, clientY: 50 }))
    await nextTick()
    expect(w.get('[aria-live="polite"]').text()).toContain('3 Mbit/s')
    await svg.trigger('pointerleave')
    expect(w.find('[aria-live="polite"]').exists()).toBe(false)
  })

  it('shows the same points as rows under Table', async () => {
    const w = chart()
    await w.get('button').trigger('click')
    expect(w.get('button').attributes('aria-pressed')).toBe('true')
    const rows = w.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[2].text()).toContain('3 Mbit/s')
    expect(w.find('svg[role="img"]').exists()).toBe(false)
  })

  // Points before the window are left out, and a window being read again
  // keeps the old frame, dimmed.
  it('draws the window only, and dims a frame being replaced', () => {
    const w = chart({ points: [[END - 400, 9e9, 9e9], ...points], stale: true })
    expect(w.text()).not.toContain('Gbit/s')
    expect(w.get('svg[role="img"]').classes()).toContain('opacity-60')
    expect(chart({ points: [] }).text()).toContain('Nothing counted yet.')
  })

  // The dashboard's placeholder: the legend's keys, grey for everything
  // read, and nothing to focus or press.
  it('draws a skeleton in grey, with nothing to read out or focus', () => {
    const w = chart({ points: null, skeleton: true })
    expect(w.text()).toContain('Down')
    expect(w.text()).not.toContain('Reading…')
    expect(w.text()).not.toContain('bit/s')
    expect(w.findAll('.skeleton')).toHaveLength(3)
    expect(w.find('button').exists()).toBe(false)
    expect(w.find('[tabindex]').exists()).toBe(false)
    expect(w.find('svg[role="img"]').exists()).toBe(false)
    const grey = w.get('svg[aria-hidden="true"] > g.animate-pulse')
    // A bar for each rate and each time, and the traffic as an area.
    expect(grey.findAll('rect').length).toBeGreaterThan(5)
    expect(grey.get('path').attributes('d')).toMatch(/^M76\.0,144\.0L.*Q.*Z$/)
  })
})
