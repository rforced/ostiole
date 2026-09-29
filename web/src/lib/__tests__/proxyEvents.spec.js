import { describe, expect, it } from 'vitest'

import { openSeconds, ruleLines } from '@/lib/proxyEvents'

describe('openSeconds', () => {
  const open = (time, logged) => openSeconds({ time, logged })

  it('says how long a request was open before the proxy logged it', () => {
    expect(open('2026-09-20T13:14:00Z', '2026-09-20T14:02:11Z')).toBe(2891)
    expect(open('2026-09-20T14:01:11Z', '2026-09-20T14:02:11Z')).toBe(60)
  })

  it('says nothing under a minute, or without both times', () => {
    expect(open('2026-09-20T14:01:12Z', '2026-09-20T14:02:11Z')).toBe(0)
    expect(open('2026-09-20T14:02:11Z', '2026-09-20T14:01:11Z')).toBe(0)
    expect(open('2026-09-20T14:02:11Z', undefined)).toBe(0)
  })
})

describe('ruleLines', () => {
  // 920450 checks each restricted header, so it matches once per header,
  // and only its first match says why.
  it('gives a rule that matched often one line, a count and all it found', () => {
    const lines = ruleLines([
      { id: 920450, message: 'HTTP header is restricted by policy', data: 'x-a' },
      { id: 920450, data: 'x-b' },
      { id: 949110, message: 'Inbound Anomaly Score Exceeded' },
      { id: 920450, data: 'x-a' },
    ])
    expect(lines).toEqual([
      { id: 920450, message: 'HTTP header is restricted by policy', data: 'x-a\nx-b', count: 3 },
      { id: 949110, message: 'Inbound Anomaly Score Exceeded', data: '', count: 1 },
    ])
  })

  it('takes the message from a later match when the first has none', () => {
    const lines = ruleLines([{ id: 1 }, { id: 1, message: 'why', data: 'what' }])
    expect(lines).toEqual([{ id: 1, message: 'why', data: 'what', count: 2 }])
  })

  it('reads an event with no rules', () => {
    expect(ruleLines(null)).toEqual([])
    expect(ruleLines(undefined)).toEqual([])
  })
})
