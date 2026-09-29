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
  parseQuick,
  peerName,
  takesDefaultRoute,
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

// The wg(8) man page's example private key, and public keys of the same shape.
const PRIVATE = 'yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk='
const SERVER = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg='
const OTHER = 'TrMvSoP4jYQlY6RIzBgbssQqY3vxI2Pi+y71lOWWXX0='

describe('parseQuick', () => {
  it('reads the file a provider hands out', () => {
    const got = parseQuick(`[Interface]
# Device: Brisk Otter
PrivateKey = ${PRIVATE}
Address = 10.64.1.2/32,fc00:bbbb:bbbb:bb01::1:2/128
DNS = 10.64.0.1

[Peer]
PublicKey = ${SERVER}
AllowedIPs = 0.0.0.0/0,::0/0
Endpoint = 198.51.100.7:51820
`)
    expect(got.errors).toEqual([])
    expect(got.iface).toEqual({
      privateKey: PRIVATE,
      ipv4: '10.64.1.2/32',
      ipv6: 'fc00:bbbb:bbbb:bb01::1:2/128',
      listenPort: 0,
      mtu: 0,
      dns: ['10.64.0.1'],
    })
    expect(got.peers).toHaveLength(1)
    expect(got.peers[0]).toMatchObject({
      publicKey: SERVER,
      allowedIps: ['0.0.0.0/0', '::/0'],
      endpoint: '198.51.100.7:51820',
      keepalive: 0,
    })
    expect(takesDefaultRoute(got.peers[0].allowedIps)).toBe(true)
  })

  it('reads a site with a listen port, an MTU, its LAN and several peers', () => {
    const got = parseQuick(`[interface]
privatekey = ${PRIVATE}
listenport = 51821
mtu = 1380
address = 10.77.0.1/30

[PEER]
publickey = ${SERVER}
presharedkey = ${OTHER}
allowedips = 10.77.0.2/32, 192.168.60.0/24
endpoint = [2001:db8::7]:51820
persistentkeepalive = 25

[Peer]
PublicKey = ${OTHER}
AllowedIPs = 10.77.0.3
`)
    expect(got.errors).toEqual([])
    expect(got.iface).toMatchObject({
      listenPort: 51821,
      mtu: 1380,
      ipv4: '10.77.0.1/30',
      ipv6: '',
    })
    expect(got.peers.map((p) => p.allowedIps)).toEqual([
      ['10.77.0.2/32', '192.168.60.0/24'],
      ['10.77.0.3/32'],
    ])
    expect(got.peers[0]).toMatchObject({
      presharedKey: OTHER,
      endpoint: '[2001:db8::7]:51820',
      keepalive: 25,
    })
    expect(takesDefaultRoute(got.peers[0].allowedIps)).toBe(false)
  })

  it('adds up a key given twice and drops comments', () => {
    const got = parseQuick(`[Interface]
PrivateKey = ${PRIVATE} # the router's own
Address = 10.66.1.2/32
Address = fd66::2/128 # IPv6 too
[Peer]
PublicKey = ${SERVER}
AllowedIPs = 10.0.0.0/8
AllowedIPs = 172.16.0.0/12
`)
    expect(got.errors).toEqual([])
    expect(got.iface).toMatchObject({ ipv4: '10.66.1.2/32', ipv6: 'fd66::2/128' })
    expect(got.peers[0].allowedIps).toEqual(['10.0.0.0/8', '172.16.0.0/12'])
  })

  it('lists what it will not run or route by, with its line', () => {
    const got = parseQuick(`[Interface]
PrivateKey = ${PRIVATE}
Address = 10.66.1.2/32
PostUp = iptables -A FORWARD -i %i -j ACCEPT
Table = off
Pony = yes
[Peer]
PublicKey = ${SERVER}
AllowedIPs = 0.0.0.0/0
`)
    expect(got.errors).toEqual([])
    expect(got.ignored).toEqual([
      { line: 4, key: 'PostUp' },
      { line: 5, key: 'Table' },
      { line: 6, key: 'Pony' },
    ])
  })

  it('refuses a second address of a family, saying where', () => {
    const got = parseQuick(`[Interface]
PrivateKey = ${PRIVATE}
Address = 10.66.1.2/32, 10.66.1.3/32
[Peer]
PublicKey = ${SERVER}
AllowedIPs = 0.0.0.0/0
`)
    expect(got.errors).toEqual([
      { line: 3, message: 'more than one IPv4 address. A tunnel here holds one of each' },
    ])
  })

  it('never repeats a key it cannot read', () => {
    const got = parseQuick(`[Interface]
PrivateKey = not-a-key-${PRIVATE.slice(0, 20)}
[Peer]
PublicKey = ${SERVER}
AllowedIPs = 0.0.0.0/0
`)
    expect(got.errors[0]).toEqual({ line: 2, message: 'PrivateKey is not a WireGuard key' })
    expect(JSON.stringify(got.errors)).not.toContain(PRIVATE.slice(0, 20))
  })

  it('keeps the addresses a DNS line holds and says what it drops', () => {
    const got = parseQuick(`[Interface]
PrivateKey = ${PRIVATE}
DNS = 10.64.0.1, fc00:bbbb::1, corp.example.com
[Peer]
PublicKey = ${SERVER}
AllowedIPs = 0.0.0.0/0
`)
    expect(got.iface.dns).toEqual(['10.64.0.1', 'fc00:bbbb::1'])
    expect(got.notes).toEqual(['Search domains in DNS are not used: corp.example.com.'])
  })

  it('says what is missing', () => {
    expect(parseQuick('').errors.map((e) => e.message)).toEqual([
      'there is no [Interface] section',
      'there is no [Peer] section',
    ])
    expect(
      parseQuick(`Address = 10.66.1.2/32
[Interface]
PrivateKey = ${PRIVATE}
[Peer]
Endpoint = 198.51.100.7
`).errors,
    ).toEqual([
      { line: 1, message: 'Address comes before any section' },
      { line: 5, message: 'Endpoint must be host:port or [IPv6]:port' },
      { line: 4, message: 'this [Peer] has no PublicKey' },
      { line: 4, message: 'this [Peer] has no AllowedIPs' },
    ])
  })
})

describe('peerName', () => {
  it('names a peer after the file it came in', () => {
    expect(peerName('se-got-wg-001.conf')).toBe('se_got_wg_001')
    expect(peerName('Home Office.CONF')).toBe('home_office')
    expect(peerName('42.conf')).toBe('peer_42')
    expect(peerName('')).toBe('server')
    expect(peerName(`${'a'.repeat(40)}.conf`)).toHaveLength(31)
  })
})
