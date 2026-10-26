import { describe, expect, it } from 'vitest'

import { actionTone, tone } from '@/lib/badge'

describe('tone', () => {
  it('colours a word by whether what it reports is fine', () => {
    expect(tone('running')).toBe('badge-ok')
    expect(tone('online')).toBe('badge-ok')
    expect(tone('current')).toBe('badge-ok')
    expect(tone('down')).toBe('badge-warn')
    expect(tone('stale')).toBe('badge-warn')
    expect(tone('no address')).toBe('badge-warn')
    expect(tone('failed')).toBe('badge-bad')
  })

  it('leaves a state that is neither good nor bad neutral', () => {
    for (const w of ['disabled', 'off', 'offline', 'quiet', 'probing', 'pending']) {
      expect(tone(w)).toBe('')
    }
  })
})

describe('actionTone', () => {
  it('colours the verdict', () => {
    expect(actionTone('accept')).toBe('badge-ok')
    expect(actionTone('reject')).toBe('badge-warn')
    expect(actionTone('drop')).toBe('badge-bad')
    expect(actionTone('')).toBe('')
  })
})
