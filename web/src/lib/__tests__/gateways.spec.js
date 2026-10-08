import { describe, expect, it } from 'vitest'

import {
  addProbe,
  eventFor,
  eventText,
  historyRows,
  historySeries,
  measured,
  neverHint,
  probeFigures,
  stateOf,
} from '@/lib/gateways'

const report = {
  families: [
    {
      family: 'IPv6',
      points: [
        [60, 7, 6, 8, 0],
        [180, null, null, null, 100],
      ],
    },
    {
      family: 'IPv4',
      points: [
        [60, 3, 2, 4, 0],
        [120, 5, 4, 6, 10],
      ],
    },
  ],
}

describe('gateways', () => {
  it('lays both families out by time, IPv4 first, a missing one undefined', () => {
    const { families, rows } = historyRows(report)
    expect(families).toEqual(['IPv4', 'IPv6'])
    expect(rows).toEqual([
      [60, 3, 2, 4, 7, 6, 8, 0, 0],
      [120, 5, 4, 6, undefined, undefined, undefined, 10, undefined],
      [180, undefined, undefined, undefined, null, null, null, undefined, 100],
    ])
    const { series, bars } = historySeries(families)
    expect(series.map((s) => [s.index, s.band])).toEqual([
      [1, [2, 3]],
      [4, [5, 6]],
    ])
    expect(bars.map((b) => [b.index, b.name])).toEqual([
      [7, 'IPv4 lost'],
      [8, 'IPv6 lost'],
    ])
  })

  it('adds a probe to its second, and lets go of what left the window', () => {
    let rows = [[800, 3, 3, 3, 0]]
    rows = addProbe(
      rows,
      ['IPv4'],
      { family: 'IPv4', time: new Date(1_200_000).toISOString(), latencyMs: 4 },
      '5m',
    )
    expect(rows).toEqual([[1200, 4, 4, 4, 0]])
    rows = addProbe(
      rows,
      ['IPv4'],
      { family: 'IPv4', time: new Date(1_205_000).toISOString(), lost: true },
      '5m',
    )
    expect(rows.at(-1)).toEqual([1205, null, null, null, 100])
    expect(
      addProbe(rows, ['IPv4'], { family: 'IPv6', time: new Date(1_210_000).toISOString() }, '5m'),
    ).toBe(rows)
  })

  it('works out five minutes of probes', () => {
    const rows = [
      [0, 2, 2, 2, 0],
      [5, 4, 4, 4, 0],
      [10, null, null, null, 100],
      [15, 6, 6, 6, 0],
    ]
    expect(probeFigures(rows, 0, 1)).toEqual({ sent: 4, mean: 4, worst: 6, loss: 25 })
  })

  it('says what was measured and how a gateway stands', () => {
    expect(measured('')).toBe('to the next hop')
    expect(measured('', 2)).toBe('to both next hops')
    expect(measured('192.0.2.53', 2)).toBe('to 192.0.2.53')
    expect(stateOf({ unknown: true })).toBe('probing')
    expect(stateOf({ online: true })).toBe('up')
    expect(stateOf({ neverAnswered: true })).toBe('never answered')
    expect(stateOf({})).toBe('down')
    expect(neverHint({})).toBe('Set a monitor address, or remove the gateway.')
    expect(neverHint({ monitor: '192.0.2.53' })).toBe(
      'Check the monitor address, or remove the gateway.',
    )
  })

  it('says what each event was', () => {
    expect(eventText({ kind: 'up' })).toBe('Up again')
    expect(eventText({ kind: 'family-down', family: 'IPv6' })).toBe('IPv6 stopped answering')
    expect(eventText({ kind: 'monitor', monitor: '192.0.2.53' })).toBe(
      'Monitor changed to 192.0.2.53, was the next hop',
    )
    expect(eventText({ kind: 'slow', family: 'IPv4', latencyMs: 312.4, limit: 200 })).toBe(
      'IPv4 slow, 312 ms on average, above 200 ms',
    )
    expect(eventText({ kind: 'lossy', family: 'IPv6', lossPercent: 16.667, limit: 10 })).toBe(
      'IPv6 losing packets, 17% lost, above 10%',
    )
    expect(eventText({ kind: 'lossy-end', family: 'IPv6' })).toBe('IPv6 no longer losing packets')
    expect(eventFor({ for: 240 })).toBe('4m')
    expect(eventFor({})).toBe('')
  })
})
