import { describe, expect, it } from 'vitest'

import { formatRemotes, nextOvpnName, parseOvpn, parseRemotes } from '@/lib/ovpnconf'

// Shaped like PEM, made of nothing.
const pem = (kind: string, body: string) =>
  `-----BEGIN ${kind}-----\n${body}\n-----END ${kind}-----`
const CA = pem('CERTIFICATE', 'TUFERS1VUC1DQS1mb3ItYS10ZXN0')
const CERT = pem('CERTIFICATE', 'TUFERS1VUC1jbGllbnQtY2VydA==')
const KEY = pem('PRIVATE KEY', 'TUFERS1VUC1jbGllbnQta2V5')
const STATIC = pem('OpenVPN Static key V1', '00112233445566778899aabbccddeeff')

/** A provider's file: sign-in, compression and the way out. */
const PROVIDER = `client
dev tun
proto udp
remote vpn.example 1194
remote 192.0.2.10 443 tcp
remote-random
resolv-retry infinite
nobind
persist-key
persist-tun
comp-lzo
auth-user-pass
redirect-gateway def1
verb 3
<ca>
${CA}
</ca>
`

/** A site file: inline certificate and key, tls-crypt, routes, keepalive. */
const SITE = `# lab-server, made for this test
client
dev tun
remote lab-server.vpn.example 1194 udp
remote-cert-tls server
verify-x509-name "lab-server" name
data-ciphers AES-256-GCM:CHACHA20-POLY1305
keepalive 15 90
route 10.20.0.0 255.255.0.0
route 10.30.1.0 255.255.255.0 vpn_gateway
route-ipv6 fd20::/64
setenv opt block-outside-dns
<ca>
${CA}
</ca>
<cert>
${CERT}
</cert>
<key>
${KEY}
</key>
<tls-crypt>
${STATIC}
</tls-crypt>
`

describe('parseOvpn', () => {
  it('reads a site file into the openvpn block', () => {
    const got = parseOvpn(SITE)
    expect(got.ok).toBe(true)
    expect(got.refused).toEqual([])
    expect(got.wayOut).toBe(false)
    expect(got.needsCredentials).toBe(false)
    expect(got.tunnel).toEqual({
      remotes: [{ host: 'lab-server.vpn.example', port: 1194 }],
      caPem: `${CA}\n`,
      certPem: `${CERT}\n`,
      keyPem: `${KEY}\n`,
      tlsCryptKeyPem: `${STATIC}\n`,
      verifyName: 'lab-server',
      dataCiphers: ['AES-256-GCM', 'CHACHA20-POLY1305'],
      keepalive: 15,
      routes: ['10.20.0.0/16', '10.30.1.0/24', 'fd20::/64'],
    })
    expect(got.dropped).toEqual([{ line: 12, text: 'setenv' }])
  })

  it('refuses compression and lists what is dropped, with lines and no values', () => {
    const got = parseOvpn(PROVIDER)
    expect(got.ok).toBe(false)
    expect(got.refused).toEqual([
      {
        line: 11,
        text: 'comp-lzo',
        reason: 'compresses traffic, a 2.4-era setting that leaks what is sent',
      },
    ])
    expect(got.dropped).toEqual([
      { line: 7, text: 'resolv-retry' },
      { line: 8, text: 'nobind' },
      { line: 9, text: 'persist-key' },
      { line: 10, text: 'persist-tun' },
      { line: 14, text: 'verb' },
    ])
    expect(got.needsCredentials).toBe(true)
    expect(got.wayOut).toBe(true)
    expect(got.tunnel.remotes).toEqual([
      { host: 'vpn.example', port: 1194 },
      { host: '192.0.2.10', port: 443, proto: 'tcp' },
    ])
    expect(got.tunnel.remoteRandom).toBe(true)
  })

  it('takes a provider file once compression is gone', () => {
    const got = parseOvpn(PROVIDER.replace('comp-lzo\n', ''))
    expect(got.ok).toBe(true)
    expect(got.tunnel.certPem).toBeUndefined()
  })

  it('refuses scripts and files by line, never quoting them', () => {
    const got = parseOvpn(
      `client\nremote vpn.example 1194\nup "/etc/secret-thing.sh"\nca ca.crt\n<cert>\n${CERT}\n</cert>\n<key>\n${KEY}\n</key>\n`,
    )
    expect(got.ok).toBe(false)
    expect(got.refused.map((r) => [r.line, r.text])).toEqual([
      [3, 'up'],
      [4, 'ca'],
      [0, '<ca>'],
    ])
    expect(JSON.stringify(got.refused)).not.toContain('secret-thing')
    expect(JSON.stringify(got.refused)).not.toContain('ca.crt')
    expect(got.refused[1].reason).toBe('names a file; put the file itself between <ca> and </ca>')
  })

  it('refuses tap, static keys, proxies and an encrypted key', () => {
    const got = parseOvpn(
      `dev tap\nsecret static.key\nhttp-proxy proxy.example 3128\nremote vpn.example\n<ca>\n${CA}\n</ca>\n<cert>\n${CERT}\n</cert>\n<key>\n${pem('ENCRYPTED PRIVATE KEY', 'bm9wZQ==')}\n</key>\n`,
    )
    expect(got.refused.map((r) => r.text)).toEqual([
      'dev',
      'secret',
      'http-proxy',
      '<key>',
      '<key>',
    ])
  })

  it('takes allow-compression no and refuses any other', () => {
    const base = `remote vpn.example\nauth-user-pass\n<ca>\n${CA}\n</ca>\n`
    expect(parseOvpn(`allow-compression no\n${base}`).ok).toBe(true)
    expect(parseOvpn(`allow-compression asym\n${base}`).refused[0].text).toBe('allow-compression')
  })

  it('folds ping into keepalive and takes the default port and proto', () => {
    const got = parseOvpn(
      `proto tcp-client\nport 443\nping 20\nping-restart 120\nremote vpn.example\nauth-user-pass\n<ca>\n${CA}\n</ca>\n`,
    )
    expect(got.ok).toBe(true)
    expect(got.tunnel.keepalive).toBe(20)
    expect(got.tunnel.remotes).toEqual([{ host: 'vpn.example', port: 443, proto: 'tcp' }])
  })

  it('takes tls-auth with its direction, or 1 with a note', () => {
    const base = `remote vpn.example\nauth-user-pass\n<ca>\n${CA}\n</ca>\n<tls-auth>\n${STATIC}\n</tls-auth>\n`
    expect(parseOvpn(`key-direction 0\n${base}`).tunnel.keyDirection).toBe(0)
    const got = parseOvpn(base)
    expect(got.tunnel.keyDirection).toBe(1)
    expect(got.notes).toHaveLength(1)
  })

  it('keeps a non-AEAD fallback after the defaults, with a note', () => {
    const got = parseOvpn(
      `remote vpn.example\ndata-ciphers-fallback AES-256-CBC\nauth-user-pass\n<ca>\n${CA}\n</ca>\n`,
    )
    expect(got.tunnel.dataCiphers).toEqual([
      'AES-256-GCM',
      'AES-128-GCM',
      'CHACHA20-POLY1305',
      'AES-256-CBC',
    ])
    expect(got.notes).toEqual([
      'A CBC cipher turns the kernel data channel off when the server picks it.',
    ])
  })

  it('leaves out ciphers this router does not offer, and refuses a list of none', () => {
    const base = `remote vpn.example\nauth-user-pass\n<ca>\n${CA}\n</ca>\n`
    const got = parseOvpn(`data-ciphers aes-256-gcm:BF-CBC:AES-256-GCM\n${base}`)
    expect(got.tunnel.dataCiphers).toEqual(['AES-256-GCM'])
    expect(got.notes).toEqual([
      'BF-CBC on line 1 are not offered by this router and were left out.',
    ])
    expect(parseOvpn(`data-ciphers BF-CBC\n${base}`).refused[0]).toMatchObject({
      line: 1,
      text: 'data-ciphers',
    })
  })

  it('takes a default route, strips comments inside a block and refuses an odd name', () => {
    const got = parseOvpn(
      `remote vpn.example\nroute 0.0.0.0 0.0.0.0\nauth-user-pass\n<ca>\n# the lab CA\n${CA}\n</ca>\n`,
    )
    expect(got.ok).toBe(true)
    expect(got.tunnel.routes).toEqual(['0.0.0.0/0'])
    expect(got.tunnel.caPem).toBe(`${CA}\n`)
    const odd = parseOvpn(
      `remote vpn.example\nverify-x509-name 'lab"server' name\nauth-user-pass\n<ca>\n${CA}\n</ca>\n`,
    )
    expect(odd.refused).toEqual([
      {
        line: 2,
        text: 'verify-x509-name',
        reason: 'holds a quote, a backslash or a control character',
      },
    ])
  })

  it('fills credentials from an inline auth-user-pass block', () => {
    const got = parseOvpn(
      `remote vpn.example\n<auth-user-pass>\nacme\nnot-a-real-password\n</auth-user-pass>\n<ca>\n${CA}\n</ca>\n`,
    )
    expect(got.ok).toBe(true)
    expect(got.needsCredentials).toBe(true)
    expect(got.tunnel.username).toBe('acme')
    expect(got.tunnel.password).toBe('not-a-real-password')
  })

  it('says what a file without a server, a CA or a way to sign in lacks', () => {
    const got = parseOvpn('client\ndev tun\n')
    expect(got.ok).toBe(false)
    expect(got.refused.map((r) => r.text)).toEqual(['remote', '<ca>', '<cert>'])
    expect(got.refused.every((r) => r.line === 0)).toBe(true)
  })

  it('refuses a block left open', () => {
    expect(parseOvpn(`remote vpn.example\n<ca>\n${CA}\n`).refused[0]).toMatchObject({
      line: 2,
      text: '<ca>',
    })
  })
})

describe('remotes as lines', () => {
  it('round-trips', () => {
    const remotes = [
      { host: 'vpn.example', port: 1194 },
      { host: 'lab-server', port: 443, proto: 'tcp' },
    ]
    expect(formatRemotes(remotes)).toBe('vpn.example 1194 udp\nlab-server 443 tcp')
    expect(parseRemotes(formatRemotes(remotes))).toEqual({ remotes, error: '' })
  })

  it('names the line that will not do', () => {
    expect(parseRemotes('vpn.example 1194 udp\nlab-server 99999 tcp').error).toBe(
      'Line 2 of the remotes must be a host, a port and udp or tcp.',
    )
    expect(parseRemotes('').error).toBe('A tunnel needs at least one remote.')
  })
})

describe('nextOvpnName', () => {
  it('takes the first free name', () => {
    expect(nextOvpnName(['eth0', 'ovpn0'])).toBe('ovpn1')
  })
})
