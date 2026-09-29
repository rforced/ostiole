/**
 * The wg-quick file a device takes to join one of this router's tunnels,
 * and the addresses that go in it.
 */

/**
 * The file: the device's own key and addresses, and this router as its
 * one peer.
 *
 * @param {{
 *   privateKey: string,
 *   addresses: string[],
 *   dns?: string[],
 *   peer: {publicKey: string, presharedKey?: string, allowedIps: string[], endpoint?: string, keepalive?: number},
 * }} file
 * @returns {string}
 */
export function deviceFile({ privateKey, addresses, dns = [], peer }) {
  const lines = ['[Interface]', `PrivateKey = ${privateKey}`, `Address = ${addresses.join(', ')}`]
  if (dns.length) lines.push(`DNS = ${dns.join(', ')}`)
  lines.push('', '[Peer]', `PublicKey = ${peer.publicKey}`)
  if (peer.presharedKey) lines.push(`PresharedKey = ${peer.presharedKey}`)
  lines.push(`AllowedIPs = ${peer.allowedIps.join(', ')}`)
  if (peer.endpoint) lines.push(`Endpoint = ${peer.endpoint}`)
  if (peer.keepalive) lines.push(`PersistentKeepalive = ${peer.keepalive}`)
  return `${lines.join('\n')}\n`
}

/** An address as a number and its width in bits, or null. */
export function parseAddress(text) {
  const s = String(text ?? '').trim()
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) {
    const parts = s.split('.').map(Number)
    if (parts.some((p) => p > 255)) return null
    return { value: parts.reduce((acc, p) => (acc << 8n) | BigInt(p), 0n), bits: 32 }
  }
  if (!/^[0-9a-f:]+$/i.test(s) || !s.includes(':')) return null
  const halves = s.split('::')
  if (halves.length > 2) return null
  const groups = (h) => (h ? h.split(':') : [])
  const head = groups(halves[0])
  const tail = halves.length === 2 ? groups(halves[1]) : []
  const missing = 8 - head.length - tail.length
  if ((halves.length === 2 && missing < 1) || (halves.length === 1 && missing !== 0)) return null
  const all = [...head, ...new Array(halves.length === 2 ? missing : 0).fill('0'), ...tail]
  if (all.some((g) => !/^[0-9a-f]{1,4}$/i.test(g))) return null
  return { value: all.reduce((acc, g) => (acc << 16n) | BigInt(parseInt(g, 16)), 0n), bits: 128 }
}

/** An address back as text, IPv6 with its longest run of zero groups folded. */
export function formatAddress({ value, bits }) {
  if (bits === 32) {
    return [24n, 16n, 8n, 0n].map((shift) => Number((value >> shift) & 0xffn)).join('.')
  }
  const groups = []
  for (let shift = 112n; shift >= 0n; shift -= 16n) groups.push(Number((value >> shift) & 0xffffn))
  let best = { at: -1, len: 1 }
  for (let i = 0; i < 8;) {
    let j = i
    while (j < 8 && groups[j] === 0) j += 1
    if (j - i > best.len) best = { at: i, len: j - i }
    i = j === i ? i + 1 : j
  }
  const hex = groups.map((g) => g.toString(16))
  if (best.at < 0) return hex.join(':')
  const head = hex.slice(0, best.at).join(':')
  const tail = hex.slice(best.at + best.len).join(':')
  return `${head}::${tail}`
}

/** "10.66.0.1/24" as its address and prefix length, or null. */
export function parsePrefix(text) {
  const [addr, len] = String(text ?? '').split('/')
  const a = parseAddress(addr)
  if (!a) return null
  const bits = len === undefined ? a.bits : Number(len)
  if (!Number.isInteger(bits) || bits < 0 || bits > a.bits) return null
  return { ...a, prefix: bits }
}

/** Whether an address lies in a prefix of the same family. */
function contains(net, addr) {
  if (net.bits !== addr.bits) return false
  const shift = BigInt(net.bits - net.prefix)
  return net.value >> shift === addr.value >> shift
}

/**
 * The first host address in a tunnel's network that neither the tunnel nor
 * a peer holds, as a /32 or a /128; "" when the network is full.
 *
 * @param {string} tunnelAddress the tunnel's own address, e.g. "10.66.0.1/24"
 * @param {string[]} taken what the peers list already
 */
export function nextFree(tunnelAddress, taken) {
  const net = parsePrefix(tunnelAddress)
  if (!net) return ''
  const used = [net, ...taken.map(parsePrefix).filter(Boolean)]
  const hostBits = BigInt(net.bits - net.prefix)
  const base = (net.value >> hostBits) << hostBits
  // IPv4 keeps its broadcast address; a /31 or /32 has no room for a peer.
  const last = base + (1n << hostBits) - (net.bits === 32 ? 2n : 1n)
  const limit = base + 65536n
  for (let v = base + 1n; v <= last && v < limit; v += 1n) {
    const addr = { value: v, bits: net.bits }
    const hit = used.some((u) => (u === net ? u.value === v : contains(u, addr)))
    if (!hit) return `${formatAddress(addr)}/${net.bits}`
  }
  return ''
}

/**
 * The addresses a device holds out of the ones its peer lists: single
 * addresses in the tunnel's networks, or any single address in a family the
 * tunnel has no network in. The rest are networks behind the device, which
 * the router routes to it and the device does not hold.
 *
 * @param {string[]} allowedIps the peer's allowed addresses
 * @param {string[]} tunnelAddresses the tunnel's own, e.g. "10.66.0.1/24"
 */
export function deviceAddresses(allowedIps, tunnelAddresses) {
  const nets = tunnelAddresses.map(parsePrefix).filter(Boolean)
  return allowedIps.filter((text) => {
    const a = parsePrefix(text)
    if (!a || a.prefix !== a.bits) return false
    const own = nets.filter((n) => n.bits === a.bits)
    return !own.length || own.some((n) => contains(n, a))
  })
}

/** The network a prefix covers, as text: "10.66.0.1/24" gives "10.66.0.0/24". */
export function networkOf(text) {
  const p = parsePrefix(text)
  if (!p) return ''
  const hostBits = BigInt(p.bits - p.prefix)
  return `${formatAddress({ value: (p.value >> hostBits) << hostBits, bits: p.bits })}/${p.prefix}`
}

/** Whether an address can be reached from the internet: not private, loopback or link-local. */
export function isPublic(text) {
  const a = parseAddress(text)
  if (!a) return false
  const inside = (net) => contains(parsePrefix(net), a)
  if (a.bits === 32) {
    return ![
      '0.0.0.0/8',
      '10.0.0.0/8',
      '100.64.0.0/10',
      '127.0.0.0/8',
      '169.254.0.0/16',
      '172.16.0.0/12',
      '192.168.0.0/16',
    ].some(inside)
  }
  return !['::/127', 'fc00::/7', 'fe80::/10'].some(inside)
}

/** An endpoint for a host and port, bracketing an IPv6 address. */
export function endpoint(host, port) {
  const h = String(host ?? '').trim()
  if (!h) return ''
  return h.includes(':') && !h.startsWith('[') ? `[${h}]:${port}` : `${h}:${port}`
}

/** 32 bytes in base64, the shape of every WireGuard key. */
const KEY_SHAPE = /^[A-Za-z0-9+/]{43}=$/

/** Keys wg-quick reads that this router has no use for: scripts it would run, and its own routing. */
const IGNORED = new Set(['preup', 'postup', 'predown', 'postdown', 'table', 'fwmark', 'saveconfig'])

/** A list value: comma separated, blanks dropped. */
const items = (v) =>
  v
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

/** An address with its length, a host length when it has none; null when it is no address. */
function withLength(text) {
  const p = parsePrefix(text)
  return p ? `${formatAddress(p)}/${p.prefix}` : null
}

/**
 * A wg-quick file as the tunnel it describes. Section names and keys
 * ignore case, # starts a comment, a list splits on commas and a key given
 * twice adds to it. The keys that make wg-quick run commands or route by
 * itself, and any it does not know, are listed as ignored with their line
 * numbers; nothing in a file is ever run. Problems carry a line number and
 * never the value of a key.
 *
 * @param {string} text
 */
export function parseQuick(text) {
  const iface = { privateKey: '', ipv4: '', ipv6: '', listenPort: 0, mtu: 0, dns: [] }
  const peers = []
  const ignored = []
  const errors = []
  const notes = []
  let section = ''
  let sectionLine = 0
  let interfaces = 0
  let peer = null
  const fail = (line, message) => errors.push({ line, message })

  String(text ?? '')
    .split(/\r?\n/)
    .forEach((raw, i) => {
      const line = i + 1
      const s = raw.replace(/#.*/, '').trim()
      if (!s) return
      const head = /^\[([^\]]*)\]$/.exec(s)
      if (head) {
        section = head[1].trim().toLowerCase()
        sectionLine = line
        if (section === 'interface') interfaces += 1
        else if (section === 'peer') {
          peer = {
            line,
            publicKey: '',
            presharedKey: '',
            allowedIps: [],
            endpoint: '',
            keepalive: 0,
          }
          peers.push(peer)
        } else fail(line, `[${head[1].trim()}] is not a wg-quick section`)
        return
      }
      const eq = s.indexOf('=')
      if (eq < 1) return fail(line, 'not a key = value line')
      const key = s.slice(0, eq).trim()
      const k = key.toLowerCase()
      const v = s.slice(eq + 1).trim()
      if (!section) return fail(line, `${key} comes before any section`)
      if (section !== 'interface' && section !== 'peer') return
      if (IGNORED.has(k)) return ignored.push({ line, key })
      if (section === 'interface') readInterface(line, key, k, v)
      else readPeer(line, key, k, v)
    })

  function readInterface(line, key, k, v) {
    switch (k) {
      case 'privatekey':
        if (!KEY_SHAPE.test(v)) return fail(line, 'PrivateKey is not a WireGuard key')
        iface.privateKey = v
        return
      case 'address':
        for (const a of items(v)) {
          const cidr = withLength(a)
          if (!cidr) return fail(line, `${a} is not an address`)
          const field = cidr.includes(':') ? 'ipv6' : 'ipv4'
          if (iface[field]) {
            return fail(
              line,
              `more than one ${field === 'ipv6' ? 'IPv6' : 'IPv4'} address. A tunnel here holds one of each`,
            )
          }
          iface[field] = cidr
        }
        return
      case 'listenport': {
        const n = Number(v)
        if (!Number.isInteger(n) || n < 0 || n > 65535)
          return fail(line, 'ListenPort must be 0-65535')
        iface.listenPort = n
        return
      }
      case 'mtu': {
        const n = Number(v)
        if (!Number.isInteger(n) || n < 576 || n > 65535) return fail(line, 'MTU must be 576-65535')
        iface.mtu = n
        return
      }
      case 'dns': {
        const names = []
        for (const d of items(v)) {
          if (parseAddress(d)) iface.dns.push(d)
          else names.push(d)
        }
        if (names.length) notes.push(`Search domains in DNS are not used: ${names.join(', ')}.`)
        return
      }
      default:
        ignored.push({ line, key })
    }
  }

  function readPeer(line, key, k, v) {
    switch (k) {
      case 'publickey':
        if (!KEY_SHAPE.test(v)) return fail(line, 'PublicKey is not a WireGuard key')
        peer.publicKey = v
        return
      case 'presharedkey':
        if (!KEY_SHAPE.test(v)) return fail(line, 'PresharedKey is not a WireGuard key')
        peer.presharedKey = v
        return
      case 'allowedips':
        for (const a of items(v)) {
          const cidr = withLength(a)
          if (!cidr) return fail(line, `${a} is not an address or network`)
          peer.allowedIps.push(cidr)
        }
        return
      case 'endpoint': {
        const m = /^(\[[^\]]+\]|[^:\s]+):(\d+)$/.exec(v)
        const port = m ? Number(m[2]) : 0
        if (!m || port < 1 || port > 65535)
          return fail(line, 'Endpoint must be host:port or [IPv6]:port')
        peer.endpoint = v
        return
      }
      case 'persistentkeepalive': {
        const n = v.toLowerCase() === 'off' ? 0 : Number(v)
        if (!Number.isInteger(n) || n < 0 || n > 65535) {
          return fail(line, 'PersistentKeepalive must be 0-65535 seconds')
        }
        peer.keepalive = n
        return
      }
      default:
        ignored.push({ line, key })
    }
  }

  if (!interfaces) fail(0, 'there is no [Interface] section')
  else if (interfaces > 1) fail(sectionLine, 'there is more than one [Interface] section')
  else if (!iface.privateKey) fail(0, 'the [Interface] section has no PrivateKey')
  if (!peers.length) fail(0, 'there is no [Peer] section')
  for (const p of peers) {
    if (!p.publicKey) fail(p.line, 'this [Peer] has no PublicKey')
    if (!p.allowedIps.length) fail(p.line, 'this [Peer] has no AllowedIPs')
  }
  return { iface, peers, ignored, notes, errors }
}

/**
 * The name a peer takes from the file it came in: lower case, the rest
 * as underscores, as short as a name may be. "server" when there is none.
 *
 * @param {string} fileName
 */
export function peerName(fileName) {
  const base = String(fileName ?? '')
    .replace(/\.conf$/i, '')
    .toLowerCase()
    .replace(/[^a-z0-9_]+/g, '_')
    .replace(/^_+|_+$/g, '')
  if (!base) return 'server'
  const name = /^[a-z]/.test(base) ? base : `peer_${base}`
  return name.slice(0, 31).replace(/_+$/, '')
}

/** Whether a peer's allowed addresses send it everything in some family. */
export function takesDefaultRoute(allowedIps) {
  return allowedIps.some((a) => parsePrefix(a)?.prefix === 0)
}
