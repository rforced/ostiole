/**
 * An OpenVPN client file, as a provider or another router hands it out,
 * read into the openvpn block of an interface. Nothing in it is run: what
 * this router sets itself is dropped and listed, what it will not do is
 * refused with the line and the reason. Values never appear in either.
 */

import type { OpenVPN, OpenVPNRemote } from '@/lib/types.gen'
import { parseAddress } from '@/lib/wgconf'

/** The data ciphers a tunnel offers when the file names none. */
export const DEFAULT_CIPHERS = ['AES-256-GCM', 'AES-128-GCM', 'CHACHA20-POLY1305']

/** The data ciphers a tunnel may offer. */
const CIPHERS = [
  'AES-128-GCM',
  'AES-192-GCM',
  'AES-256-GCM',
  'CHACHA20-POLY1305',
  'AES-128-CBC',
  'AES-256-CBC',
]

/** Set by this router or of no use to it. */
const DROPPED = new Set([
  'resolv-retry',
  'nobind',
  'verb',
  'mute',
  'mute-replay-warnings',
  'explicit-exit-notify',
  'reneg-sec',
  'server-poll-timeout',
  'connect-retry',
  'connect-retry-max',
  'fast-io',
  'sndbuf',
  'rcvbuf',
  'tun-mtu',
  'tun-mtu-extra',
  'mssfix',
  'cipher',
  'auth',
  'block-outside-dns',
  'service',
  'tun-ipv6',
  'setenv',
  'dhcp-option',
  'pull',
  'tls-client',
  'tls-cipher',
  'tls-ciphersuites',
  'crl-verify',
  'auth-nocache',
  'auth-retry',
  'float',
  'ping-restart',
  'ping-exit',
  'ping-timer-rem',
  'route-delay',
  'route-method',
  'route-metric',
  'route-nopull',
  'pull-filter',
  'ignore-unknown-option',
  'topology',
  'remote-cert-ku',
  'remote-cert-eku',
  'ns-cert-type',
  'user',
  'group',
  'block-ipv6',
  'tls-version-max',
])

const SCRIPT = 'runs a program on this router'
const OWN = 'is set by this router'
const FILE = 'writes a file on this router'
const KEY_ELSEWHERE = 'keeps the key outside the file'
const PROXY = 'goes through a proxy, which this router does not do'
const COMPRESSION = 'compresses traffic, a 2.4-era setting that leaks what is sent'

/** Refused whatever their arguments, with the reason. */
const REFUSED = new Map([
  ['up', SCRIPT],
  ['down', SCRIPT],
  ['route-up', SCRIPT],
  ['route-pre-down', SCRIPT],
  ['ipchange', SCRIPT],
  ['tls-verify', SCRIPT],
  ['auth-user-pass-verify', SCRIPT],
  ['learn-address', SCRIPT],
  ['client-connect', SCRIPT],
  ['client-disconnect', SCRIPT],
  ['script-security', SCRIPT],
  ['plugin', 'loads a plugin into the daemon'],
  ['management', OWN],
  ['daemon', OWN],
  ['chroot', OWN],
  ['cd', OWN],
  ['config', 'reads another file'],
  ['log', FILE],
  ['log-append', FILE],
  ['status', FILE],
  ['writepid', FILE],
  ['pkcs12', KEY_ELSEWHERE],
  ['cryptoapicert', KEY_ELSEWHERE],
  ['askpass', 'protects the private key with a passphrase, which this router cannot type'],
  ['secret', 'is a static key, which this router does not take'],
  ['fragment', 'splits packets in a way the kernel data channel cannot'],
  ['http-proxy', PROXY],
  ['http-proxy-option', PROXY],
  ['socks-proxy', PROXY],
  ['comp-lzo', COMPRESSION],
  ['compress', COMPRESSION],
  ['tls-crypt-v2', 'is not taken yet'],
])

/** The inline blocks taken, and the field each fills. */
const BLOCKS: Record<string, 'caPem' | 'certPem' | 'keyPem' | 'tlsCryptKeyPem' | 'tlsAuthKeyPem'> =
  {
    ca: 'caPem',
    cert: 'certPem',
    key: 'keyPem',
    'tls-crypt': 'tlsCryptKeyPem',
    'tls-auth': 'tlsAuthKeyPem',
  }

/** Blocks with nothing this router uses in them. */
const DROPPED_BLOCKS = new Set(['dh', 'crl-verify', 'extra-certs'])

/**
 * One line as its words: double quotes may hold spaces and backslash
 * escapes, single quotes hold anything.
 */
function words(s: string) {
  const out: string[] = []
  let i = 0
  while (i < s.length) {
    while (i < s.length && /\s/.test(s[i])) i += 1
    if (i >= s.length) break
    let w = ''
    if (s[i] === '"') {
      i += 1
      while (i < s.length && s[i] !== '"') {
        if (s[i] === '\\' && i + 1 < s.length) i += 1
        w += s[i]
        i += 1
      }
      i += 1
    } else if (s[i] === "'") {
      i += 1
      while (i < s.length && s[i] !== "'") w += s[i++]
      i += 1
    } else {
      while (i < s.length && !/\s/.test(s[i])) {
        if (s[i] === '\\' && i + 1 < s.length) i += 1
        w += s[i]
        i += 1
      }
    }
    out.push(w)
  }
  return out
}

/** udp4, udp6 and udp as udp; tcp, tcp-client and the rest as tcp; else null. */
function protoOf(p: unknown) {
  const s = String(p ?? '').toLowerCase()
  if (/^udp[46]?$/.test(s)) return 'udp'
  if (/^tcp[46]?(-client)?$/.test(s)) return 'tcp'
  return null
}

/** A dotted netmask as its prefix length, or -1. */
function maskBits(mask: string) {
  const a = parseAddress(mask)
  if (!a || a.bits !== 32) return -1
  const n = Number(a.value)
  let bits = 0
  while (bits < 32 && n & (2 ** (31 - bits))) bits += 1
  return n === (bits ? 2 ** 32 - 2 ** (32 - bits) : 0) ? bits : -1
}

const HOST = /^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*\.?$/

/** A remote's host: a name or an address. */
const isHost = (h: string) => Boolean(parseAddress(h)) || (h.length <= 253 && HOST.test(h))

export interface OvpnParse {
  ok: boolean
  tunnel: OpenVPN
  dropped: { line: number; text: string }[]
  refused: { line: number; text: string; reason: string }[]
  notes: string[]
  needsCredentials: boolean
  wayOut: boolean
}

/** A null the compiler keeps typed as a T | null, for a value only closures assign. */
const unset = <T>(): T | null => null

/** Reads the text of an .ovpn file. */
export function parseOvpn(text: string): OvpnParse {
  const tunnel: OpenVPN = { remotes: [], caPem: '' }
  const dropped: OvpnParse['dropped'] = []
  const refused: OvpnParse['refused'] = []
  const notes: string[] = []
  const remotes: { host: string; port: number; proto: string | null }[] = []
  let defaultProto = 'udp'
  let defaultPort = 1194
  let needsCredentials = false
  let wayOut = false
  let block = unset<{ name: string; line: number; lines: string[] }>()
  let ping = 0
  let fallback = unset<{ line: number; cipher: string }>()
  let ciphers = unset<{ line: number; list: string[] }>()
  const lines = String(text ?? '').split(/\r?\n/)

  const refuse = (line: number, name: string, reason: string): void => {
    refused.push({ line, text: name, reason })
  }
  const drop = (line: number, text: string): void => {
    dropped.push({ line, text })
  }

  lines.forEach((raw, idx) => {
    const line = idx + 1
    const s = raw.trim()
    if (block) {
      if (s.toLowerCase() === `</${block.name}>`) {
        endBlock(block)
        block = null
      } else block.lines.push(raw.replace(/\s+$/, ''))
      return
    }
    if (!s || s.startsWith('#') || s.startsWith(';')) return
    const open = /^<([A-Za-z0-9-]+)>$/.exec(s)
    if (open) {
      block = { name: open[1].toLowerCase(), line, lines: [] }
      return
    }
    const [first, ...args] = words(s)
    const name = first.replace(/^--/, '').toLowerCase()
    directive(line, name, args)
  })
  if (block) refuse(block.line, `<${block.name}>`, 'is never closed')

  function endBlock(b: { name: string; line: number; lines: string[] }) {
    const tag = `<${b.name}>`
    if (DROPPED_BLOCKS.has(b.name)) return drop(b.line, tag)
    if (b.name === 'auth-user-pass') {
      const [user = '', pass = ''] = b.lines.map((l) => l.trim()).filter(Boolean)
      if (user) tunnel.username = user
      if (pass) tunnel.password = pass
      needsCredentials = true
      return
    }
    const field = BLOCKS[b.name]
    if (!field) {
      if (REFUSED.has(b.name)) return refuse(b.line, tag, REFUSED.get(b.name) as string)
      return refuse(b.line, tag, 'is not a block this router takes')
    }
    const body = b.lines
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith('#') && !l.startsWith(';'))
      .join('\n')
    const pem = /-----BEGIN [A-Za-z0-9 ]+-----[\s\S]+-----END [A-Za-z0-9 ]+-----/.exec(body)
    if (!pem || body.includes('<')) return refuse(b.line, tag, 'holds no PEM block')
    if (b.name === 'key' && /ENCRYPTED/.test(pem[0])) {
      return refuse(b.line, tag, 'is protected by a passphrase, which this router cannot type')
    }
    tunnel[field] = `${body}\n`
  }

  function directive(line: number, name: string, args: string[]) {
    if (REFUSED.has(name)) return refuse(line, name, REFUSED.get(name) as string)
    if (name.startsWith('pkcs11-')) return refuse(line, name, KEY_ELSEWHERE)
    if (name.startsWith('persist-') || DROPPED.has(name)) return drop(line, name)
    switch (name) {
      case 'client':
      case 'remote-random-hostname':
        return
      case 'tls-version-min':
        return
      case 'dev':
      case 'dev-type':
        if (/^tun/i.test(args[0] ?? '')) return
        return refuse(line, name, 'is not a tun device; a layer 2 tunnel is not taken')
      case 'proto': {
        const p = protoOf(args[0])
        if (!p) return refuse(line, name, 'must be udp or tcp as a client')
        defaultProto = p
        return
      }
      case 'port':
      case 'rport': {
        const n = Number(args[0])
        if (!Number.isInteger(n) || n < 1 || n > 65535) return refuse(line, name, 'must be 1-65535')
        defaultPort = n
        return
      }
      case 'remote': {
        const [host = '', port, proto] = args
        if (!isHost(host)) return refuse(line, name, 'needs a host name or address')
        const n = port === undefined ? 0 : Number(port)
        if (port !== undefined && (!Number.isInteger(n) || n < 1 || n > 65535)) {
          return refuse(line, name, 'has a port outside 1-65535')
        }
        const p = proto === undefined ? null : protoOf(proto)
        if (proto !== undefined && !p) return refuse(line, name, 'must be udp or tcp as a client')
        remotes.push({ host, port: n, proto: p })
        return
      }
      case 'remote-random':
        tunnel.remoteRandom = true
        return
      case 'remote-cert-tls':
        if ((args[0] ?? '').toLowerCase() === 'server') return
        return refuse(line, name, "must check the server's certificate as a server's")
      case 'verify-x509-name': {
        const type = (args[1] ?? 'subject').toLowerCase()
        if (!args[0] || type !== 'name') {
          return refuse(line, name, 'is taken only as an exact name, with the type name')
        }
        // eslint-disable-next-line no-control-regex
        if (/["\\\x00-\x1f\x7f]/.test(args[0])) {
          return refuse(line, name, 'holds a quote, a backslash or a control character')
        }
        tunnel.verifyName = args[0]
        return
      }
      case 'data-ciphers':
      case 'ncp-ciphers':
        ciphers = { line, list: (args[0] ?? '').split(':').filter(Boolean) }
        return
      case 'data-ciphers-fallback':
        if (args[0]) fallback = { line, cipher: args[0] }
        return
      case 'auth-user-pass':
        needsCredentials = true
        return
      case 'key-direction': {
        const n = Number(args[0])
        if (n !== 0 && n !== 1) return refuse(line, name, 'must be 0 or 1')
        tunnel.keyDirection = n
        return
      }
      case 'keepalive': {
        const n = Number(args[0])
        if (!Number.isInteger(n) || n < 1 || n > 3600)
          return refuse(line, name, 'must be 1-3600 seconds')
        tunnel.keepalive = n
        return
      }
      case 'ping': {
        const n = Number(args[0])
        if (!Number.isInteger(n) || n < 1 || n > 3600)
          return refuse(line, name, 'must be 1-3600 seconds')
        ping = n
        return
      }
      case 'route':
        return route(line, name, args)
      case 'route-ipv6': {
        const [addr, len] = String(args[0] ?? '').split('/')
        const a = parseAddress(addr)
        const bits = Number(len)
        if (!a || a.bits !== 128 || !Number.isInteger(bits) || bits < 0 || bits > 128) {
          return refuse(line, name, 'needs an IPv6 network')
        }
        ;(tunnel.routes ??= []).push(args[0])
        return
      }
      case 'redirect-gateway':
        wayOut = true
        return
      case 'allow-compression':
        if ((args[0] ?? '').toLowerCase() === 'no') return drop(line, name)
        return refuse(line, name, COMPRESSION)
      case 'ca':
      case 'cert':
      case 'key':
      case 'tls-crypt':
      case 'tls-auth':
        if (args[0] && args[0] !== '[inline]') {
          return refuse(
            line,
            name,
            `names a file; put the file itself between <${name}> and </${name}>`,
          )
        }
        if (name === 'tls-auth' && (args[1] === '0' || args[1] === '1')) {
          tunnel.keyDirection = Number(args[1])
        }
        return
      default:
        dropped.push({ line, text: name })
    }
  }

  function route(line: number, name: string, args: string[]) {
    const [net = '', mask = '255.255.255.255'] = args
    const a = parseAddress(net)
    const bits = maskBits(mask)
    if (!a || a.bits !== 32 || bits < 0) return refuse(line, name, 'needs an IPv4 network and mask')
    ;(tunnel.routes ??= []).push(`${net}/${bits}`)
  }

  tunnel.remotes = remotes.map((r) => {
    const out: OpenVPNRemote = { host: r.host, port: r.port || defaultPort }
    const proto = r.proto ?? defaultProto
    if (proto !== 'udp') out.proto = proto
    return out
  })
  if (!tunnel.keepalive && ping) tunnel.keepalive = ping
  if (ciphers || fallback) {
    const listed = ciphers ? ciphers.list : [...DEFAULT_CIPHERS]
    if (fallback) listed.push(fallback.cipher)
    const list: string[] = []
    const unknown: string[] = []
    for (const c of listed.map((x) => x.toUpperCase())) {
      if (!CIPHERS.includes(c)) unknown.push(c)
      else if (!list.includes(c)) list.push(c)
    }
    const at = ciphers?.line ?? fallback?.line ?? 0
    if (!list.length) refuse(at, 'data-ciphers', 'names no cipher this router offers')
    else {
      tunnel.dataCiphers = list
      if (unknown.length) {
        notes.push(
          `${unknown.join(', ')} on line ${at} are not offered by this router and were left out.`,
        )
      }
      if (list.some((c) => c.endsWith('-CBC'))) {
        notes.push('A CBC cipher turns the kernel data channel off when the server picks it.')
      }
    }
  }
  if (tunnel.tlsAuthKeyPem && tunnel.keyDirection === undefined) {
    tunnel.keyDirection = 1
    notes.push("tls-auth has no key-direction; 1, a client's usual, is taken.")
  }
  if (!tunnel.tlsAuthKeyPem) delete tunnel.keyDirection

  if (!tunnel.remotes.length) refuse(0, 'remote', 'is missing, so the file names no server')
  if (!tunnel.caPem) refuse(0, '<ca>', "is missing, so the server's certificate cannot be checked")
  if (Boolean(tunnel.certPem) !== Boolean(tunnel.keyPem)) {
    refuse(0, tunnel.certPem ? '<key>' : '<cert>', 'is missing; a certificate needs its key')
  }
  if (!tunnel.certPem && !needsCredentials) {
    refuse(0, '<cert>', 'is missing, and without auth-user-pass the server cannot tell who this is')
  }
  if (tunnel.tlsCryptKeyPem && tunnel.tlsAuthKeyPem) {
    refuse(0, '<tls-auth>', 'comes with <tls-crypt>; a tunnel takes one of them')
  }
  refused.sort((x, y) => (x.line || Infinity) - (y.line || Infinity))
  return { ok: !refused.length, tunnel, dropped, refused, notes, needsCredentials, wayOut }
}

/** ovpn0, ovpn1, … whichever is free among the names given. */
export function nextOvpnName(taken: Iterable<string>) {
  const used = new Set(taken)
  for (let i = 0; i < 100; i += 1) if (!used.has(`ovpn${i}`)) return `ovpn${i}`
  return 'ovpn0'
}

/** Remotes as one line each, "host port proto". */
export function formatRemotes(remotes: OpenVPNRemote[] | null | undefined) {
  return (remotes ?? []).map((r) => `${r.host} ${r.port} ${r.proto || 'udp'}`).join('\n')
}

/**
 * The lines formatRemotes makes, back as remotes, or an error naming the
 * line. The default protocol is left out, as the model has it.
 */
export function parseRemotes(text: string): { remotes: OpenVPNRemote[]; error: string } {
  const remotes: OpenVPNRemote[] = []
  const rows = String(text ?? '').split('\n')
  for (let i = 0; i < rows.length; i += 1) {
    const s = rows[i].trim()
    if (!s) continue
    const [host = '', port = '1194', proto = 'udp', ...rest] = s.split(/\s+/)
    const n = Number(port)
    const p = protoOf(proto)
    if (rest.length || !isHost(host) || !Number.isInteger(n) || n < 1 || n > 65535 || !p) {
      return {
        remotes: [],
        error: `Line ${i + 1} of the remotes must be a host, a port and udp or tcp.`,
      }
    }
    remotes.push(p === 'udp' ? { host, port: n } : { host, port: n, proto: p })
  }
  if (!remotes.length) return { remotes, error: 'A tunnel needs at least one remote.' }
  return { remotes, error: '' }
}
