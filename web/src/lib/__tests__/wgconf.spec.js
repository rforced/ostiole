import { describe, expect, it } from 'vitest'

import {
  deviceAddresses,
  deviceFile,
  endpoint,
  formatAddress,
  isPublic,
  networkOf,
  nextFree,
  parseAddress,
} from '@/lib/wgconf'

describe('wgconf', () => {
  it('writes the file a device takes', () => {
    expect(
      deviceFile({
        privateKey: 'PRIVATE',
        addresses: ['10.66.0.2/32', 'fd66::2/128'],
        dns: ['10.66.0.1'],
        peer: {
          publicKey: 'ROUTER',
          presharedKey: 'PSK',
          allowedIps: ['0.0.0.0/0', '::/0'],
          endpoint: 'vpn.example.com:51820',
          keepalive: 25,
        },
      }),
    ).toBe(
      [
        '[Interface]',
        'PrivateKey = PRIVATE',
        'Address = 10.66.0.2/32, fd66::2/128',
        'DNS = 10.66.0.1',
        '',
        '[Peer]',
        'PublicKey = ROUTER',
        'PresharedKey = PSK',
        'AllowedIPs = 0.0.0.0/0, ::/0',
        'Endpoint = vpn.example.com:51820',
        'PersistentKeepalive = 25',
        '',
      ].join('\n'),
    )
  })

  it('leaves out what a device does not need', () => {
    const text = deviceFile({
      privateKey: 'K',
      addresses: ['10.66.0.2/32'],
      peer: { publicKey: 'R', allowedIps: ['10.66.0.0/24'] },
    })
    expect(text).not.toMatch(/DNS|PresharedKey|Endpoint|PersistentKeepalive/)
  })

  it('reads and writes both families', () => {
    for (const s of ['10.66.0.1', '::', '::1', 'fd66::2', '2001:db8:0:1:0:0:0:1', 'fe80::1:2']) {
      expect(formatAddress(parseAddress(s))).toBe(
        s === '2001:db8:0:1:0:0:0:1' ? '2001:db8:0:1::1' : s,
      )
    }
    expect(formatAddress(parseAddress('1:0:0:2:0:0:0:3'))).toBe('1:0:0:2::3')
    for (const bad of ['10.66.0.256', '1::2::3', 'nope', '1:2:3:4:5:6:7:8:9', '']) {
      expect(parseAddress(bad)).toBeNull()
    }
  })

  it('finds the next free address in each family', () => {
    expect(nextFree('10.66.0.1/24', ['10.66.0.2/32', '10.66.0.3/32'])).toBe('10.66.0.4/32')
    expect(nextFree('10.66.0.1/24', ['10.66.0.0/30'])).toBe('10.66.0.4/32')
    expect(nextFree('10.66.0.5/24', [])).toBe('10.66.0.1/32')
    expect(nextFree('fd66::1/64', ['fd66::2/128'])).toBe('fd66::3/128')
    // A /30 has two hosts, the tunnel's and one more; a /31 has no room.
    expect(nextFree('10.77.0.1/30', [])).toBe('10.77.0.2/32')
    expect(nextFree('10.77.0.1/30', ['10.77.0.2/32'])).toBe('')
    expect(nextFree('10.77.0.0/31', [])).toBe('')
    expect(nextFree('', [])).toBe('')
  })

  it('gives a device only the addresses it holds', () => {
    const tunnel = ['10.66.0.1/24', 'fd66::1/64']
    // The router's own LAN, and networks and a host behind the device.
    const listed = ['10.66.0.2/32', '192.168.1.1/24', '192.168.50.0/24', '192.168.50.7/32']
    expect(deviceAddresses([...listed, 'fd66::2/128'], tunnel)).toEqual([
      '10.66.0.2/32',
      'fd66::2/128',
    ])
    // A /24 in the tunnel's network is a network, not an address.
    expect(deviceAddresses(['10.66.0.5/24'], tunnel)).toEqual([])
    // Where the tunnel has no network, any single address will do.
    expect(
      deviceAddresses(['10.99.0.2/32', '10.99.1.0/24', 'fd99::2/128'], ['fd66::1/64']),
    ).toEqual(['10.99.0.2/32'])
    expect(deviceAddresses(['10.99.0.2/32'], [])).toEqual(['10.99.0.2/32'])
  })

  it('names networks, public addresses and endpoints', () => {
    expect(networkOf('10.66.0.1/24')).toBe('10.66.0.0/24')
    expect(networkOf('fd66::1/64')).toBe('fd66::/64')
    expect(['203.0.113.7', '2001:db8::1'].every(isPublic)).toBe(true)
    expect(['192.168.1.1', '100.64.0.1', 'fe80::1', 'fd00::1', '10.0.0.1'].some(isPublic)).toBe(
      false,
    )
    expect(endpoint('2001:db8::1', 51820)).toBe('[2001:db8::1]:51820')
    expect(endpoint('vpn.example.com', 51820)).toBe('vpn.example.com:51820')
  })
})
