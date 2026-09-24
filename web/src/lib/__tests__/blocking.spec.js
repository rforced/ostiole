import { describe, expect, it } from 'vitest'

import { coversName, exceptionToggles, isDomainName, protectedNames } from '@/lib/blocking'

/** The router TestNeverBlockedCoversTheExpandedNames describes in Go. */
function router(blocking = {}) {
  return {
    system: { hostname: 'gateway' },
    services: {
      dns: {
        domain: 'lan',
        hostOverrides: [
          { hostname: 'printer', ip: '192.168.1.5', aliases: ['print'] },
          { hostname: 'potato', domain: 'test', ip: '192.168.1.6' },
        ],
        domainOverrides: [{ domain: 'ts.net', servers: ['100.100.100.100'] }],
      },
      dhcp: { staticLeases: [{ mac: 'aa:bb:cc:dd:ee:01', hostname: 'nas' }] },
    },
    blocking: { enabled: true, ...blocking },
  }
}

describe('protectedNames', () => {
  it('matches the names Go never blocks', () => {
    const got = protectedNames(router())
    for (const want of [
      'lan',
      'gateway',
      'gateway.lan',
      'printer',
      'printer.lan',
      'print',
      'print.lan',
      'nas',
      'nas.lan',
      'potato.test',
      'ts.net',
    ]) {
      expect(got, want).toContain(want)
    }
    // Another domain answers in full only.
    expect(got).not.toContain('potato')
    expect(got).not.toContain('potato.test.lan')
  })

  it('has nothing to add without a local domain', () => {
    expect(protectedNames({ system: { hostname: 'gateway' } })).toEqual(['gateway'])
    expect(protectedNames(null)).toEqual([])
  })
})

describe('isDomainName', () => {
  it('takes what the check takes', () => {
    expect(isDomainName('ads.example.com')).toBe(true)
    expect(isDomainName('Ads.Example.COM.')).toBe(true)
    expect(isDomainName('wpad')).toBe(true)
    for (const bad of [
      '',
      '.',
      '_dns.resolver.arpa',
      '-ads.example.com',
      `${'a'.repeat(64)}.com`,
    ]) {
      expect(isDomainName(bad), bad).toBe(false)
    }
  })
})

describe('coversName', () => {
  it('covers the name and everything under it', () => {
    expect(coversName('example.com', 'example.com')).toBe(true)
    expect(coversName('Example.com.', 'ads.example.com')).toBe(true)
    expect(coversName('example.com', 'badexample.com')).toBe(false)
    expect(coversName('ads.example.com', 'example.com')).toBe(false)
    expect(coversName('', 'example.com')).toBe(false)
  })
})

describe('exceptionToggles', () => {
  const blocked = (name) => ({ name, status: 'blocked' })
  const answered = (name) => ({ name, status: 'ok' })

  it('offers never block for a blocked answer and always block for the rest', () => {
    const toggle = exceptionToggles(router())
    expect(toggle(blocked('ads.example.com'))).toEqual({ key: 'allow', on: false })
    expect(toggle(answered('example.com'))).toEqual({ key: 'deny', on: false })
    expect(toggle({ name: 'gone.example.com', status: 'nxdomain' })).toEqual({
      key: 'deny',
      on: false,
    })
  })

  it('ticks the list a name is on, whatever became of the query', () => {
    const toggle = exceptionToggles(
      router({ allow: ['Ads.Example.com.'], deny: ['tracker.example.net'] }),
    )
    expect(toggle(blocked('ads.example.com'))).toEqual({ key: 'allow', on: true })
    expect(toggle(answered('ads.example.com'))).toEqual({ key: 'allow', on: true })
    expect(toggle(blocked('tracker.example.net'))).toEqual({ key: 'deny', on: true })
    expect(toggle(answered('tracker.example.net'))).toEqual({ key: 'deny', on: true })
  })

  it('shows allow for a name on both lists, because allow wins', () => {
    const toggle = exceptionToggles(router({ allow: ['both.example'], deny: ['both.example'] }))
    expect(toggle(blocked('both.example'))).toEqual({ key: 'allow', on: true })
  })

  it('offers nothing the check would refuse', () => {
    const toggle = exceptionToggles(router())
    expect(toggle(answered('_dns.resolver.arpa'))).toBeNull()
    expect(toggle(blocked('_dns.resolver.arpa'))).toBeNull()
    // Above a name the router answers for, or a delegated domain.
    expect(toggle(answered('test'))).toBeNull()
    expect(toggle(answered('net'))).toBeNull()
  })

  it('offers nothing for what the router answers for or has delegated', () => {
    const toggle = exceptionToggles(router())
    expect(toggle(answered('nas.lan'))).toBeNull()
    expect(toggle(answered('anything.lan'))).toBeNull()
    expect(toggle(answered('potato.test'))).toBeNull()
    expect(toggle(answered('host.ts.net'))).toBeNull()
    expect(toggle(blocked('nas.lan'))).toBeNull()
    // Unrelated names that only look alike.
    expect(toggle(answered('notlan'))).toEqual({ key: 'deny', on: false })
    expect(toggle(answered('www.potato.example'))).toEqual({ key: 'deny', on: false })
  })

  it('leaves always block off under an allowed parent, which would win', () => {
    const toggle = exceptionToggles(router({ allow: ['example.com'] }))
    expect(toggle(answered('cdn.example.com'))).toBeNull()
    // A blocked answer from before the parent was allowed still offers it.
    expect(toggle(blocked('ads.example.com'))).toEqual({ key: 'allow', on: false })
    // Under a denied parent, never block is what beats it.
    const denied = exceptionToggles(router({ deny: ['example.org'] }))
    expect(denied(blocked('www.example.org'))).toEqual({ key: 'allow', on: false })
  })
})
