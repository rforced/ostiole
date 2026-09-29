import { describe, expect, it } from 'vitest'

import {
  fittingTicks,
  linePath,
  nearest,
  niceScale,
  rateTicks,
  timeLabel,
  timeTicks,
} from '@/lib/chart'

describe('niceScale', () => {
  // The axis stops at round rates, about four of them.
  it('rounds the top to a step of 1, 2, 2.5 or 5', () => {
    expect(niceScale(12_345_678)).toEqual({ top: 15_000_000, step: 5_000_000 })
    expect(niceScale(9_000)).toEqual({ top: 10_000, step: 2_500 })
    expect(niceScale(400_000_000)).toEqual({ top: 400_000_000, step: 100_000_000 })
    expect(rateTicks(niceScale(12_345_678))).toEqual([0, 5e6, 10e6, 15e6])
  })

  it('gives nothing moving an axis of a kilobit', () => {
    expect(niceScale(0)).toEqual({ top: 1000, step: 250 })
    expect(rateTicks(niceScale(0))).toHaveLength(5)
  })
})

describe('timeTicks', () => {
  const at = (y, mo, d, h, mi = 0, s = 0) => new Date(y, mo - 1, d, h, mi, s).getTime() / 1000

  // Local minutes, hours and midnights: what a reader rounds to.
  it('stops at whole minutes, hours and midnights', () => {
    const end = at(2026, 9, 27, 14, 7, 30)
    const five = timeTicks(end - 300, end, '5m')
    expect(five.map((t) => new Date(t * 1000).getMinutes())).toEqual([3, 4, 5, 6, 7])
    const day = timeTicks(end - 86400, end, '24h')
    expect(day.every((t) => new Date(t * 1000).getHours() % 4 === 0)).toBe(true)
    expect(day).toHaveLength(6)
    const month = timeTicks(end - 31 * 86400, end, '31d')
    expect(month.every((t) => new Date(t * 1000).getHours() === 0)).toBe(true)
    expect(month.length).toBeGreaterThanOrEqual(6)
    // A narrow chart takes every other stop.
    expect(timeTicks(end - 300, end, '5m', true)).toHaveLength(2)
  })

  it('names a stop by the time within a day and the date over a month', () => {
    const t = at(2026, 9, 27, 14, 5)
    expect(timeLabel(t, '24h')).toBe(
      new Date(t * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }),
    )
    expect(timeLabel(t, '31d')).toBe(
      new Date(t * 1000).toLocaleDateString(undefined, { day: 'numeric', month: 'short' }),
    )
  })
})

describe('fittingTicks', () => {
  // A label is centred on its stop, so one near either end would run off.
  it('keeps the stops whose label fits inside the chart', () => {
    // Eight characters at 7 px: 28 px either side of the stop.
    const label = () => '02:57 AM'
    const x = (t) => t
    expect(fittingTicks([27, 28, 300, 612, 613], x, label, 640)).toEqual([28, 300, 612])
  })
})

describe('nearest', () => {
  const pts = [
    [10, 1, 1],
    [20, 2, 2],
    [30, 3, 3],
  ]
  it('finds the point closest in time', () => {
    expect(nearest(pts, 0)).toBe(0)
    expect(nearest(pts, 14)).toBe(0)
    expect(nearest(pts, 16)).toBe(1)
    expect(nearest(pts, 99)).toBe(2)
    expect(nearest([], 5)).toBe(-1)
  })

  it('draws a path through one value of each point', () => {
    expect(
      linePath(
        pts,
        2,
        (t) => t,
        (v) => 100 - v,
      ),
    ).toBe('M10.0,99.0L20.0,98.0L30.0,97.0')
    expect(linePath([], 1, Number, Number)).toBe('')
  })
})
