import { describe, expect, it } from 'vitest'

import { isRandomMAC } from '@/lib/mac'

describe('isRandomMAC', () => {
  it('reads the locally administered bit', () => {
    expect(isRandomMAC('da:a1:19:00:00:01')).toBe(true)
    expect(isRandomMAC('02:00:00:00:00:01')).toBe(true)
    expect(isRandomMAC('AE:00:00:00:00:01')).toBe(true)
    expect(isRandomMAC('3c:0a:f3:60:c4:60')).toBe(false)
    expect(isRandomMAC('00:00:00:00:00:02')).toBe(false)
  })

  // Guests and containers set the bit on addresses that stay put.
  it('leaves out libvirt and Docker', () => {
    expect(isRandomMAC('52:54:00:12:34:56')).toBe(false)
    expect(isRandomMAC('02:42:ac:11:00:02')).toBe(false)
  })

  it('says nothing of what is not a MAC', () => {
    expect(isRandomMAC('')).toBe(false)
    expect(isRandomMAC(undefined)).toBe(false)
    expect(isRandomMAC('02:01:00:01:2c:00:00:00:aa:bb')).toBe(false)
    expect(isRandomMAC('02:00:00:00:00')).toBe(false)
  })
})
