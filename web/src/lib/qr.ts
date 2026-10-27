/**
 * A QR code encoder for the device files WireGuard hands out: byte mode,
 * error correction level M, the smallest version from 1 to 40 that holds
 * the text, and the mask with the lowest penalty. Written from ISO/IEC
 * 18004 rather than taken from a package.
 */

/** Error correction codewords per block at level M, by version. */
const ECC_PER_BLOCK = [
  0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28,
  28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28,
]

/** Blocks at level M, by version. */
const BLOCKS = [
  0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25,
  26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49,
]

/** Level M's two format bits. */
const LEVEL_M = 0b00

/** A table entry by version; NaN outside the table, as an unchecked read would give. */
const entry = (table: number[], version: number) => table[version] ?? Number.NaN

/** The modules one version has for data and error correction, in bits. */
function rawModules(version: number) {
  let n = (16 * version + 128) * version + 64
  if (version >= 2) {
    const align = Math.floor(version / 7) + 2
    n -= (25 * align - 10) * align - 55
    if (version >= 7) n -= 36
  }
  return n
}

/** Data codewords at level M. */
export function dataCodewords(version: number) {
  return (
    Math.floor(rawModules(version) / 8) - entry(ECC_PER_BLOCK, version) * entry(BLOCKS, version)
  )
}

/** How many bytes a version holds in byte mode at level M. */
export function capacity(version: number) {
  const countBits = version < 10 ? 8 : 16
  return Math.floor((dataCodewords(version) * 8 - 4 - countBits) / 8)
}

/** The product of two elements of GF(2^8) modulo x^8 + x^4 + x^3 + x^2 + 1. */
function gfMultiply(x: number, y: number) {
  let z = 0
  for (let i = 7; i >= 0; i -= 1) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d)
    z ^= ((y >>> i) & 1) * x
  }
  return z
}

/** The generator polynomial of a degree, highest term dropped, as coefficients. */
function rsDivisor(degree: number) {
  const out = new Array<number>(degree).fill(0)
  out[degree - 1] = 1
  let root = 1
  for (let i = 0; i < degree; i += 1) {
    for (let j = 0; j < out.length; j += 1) {
      out[j] = gfMultiply(out[j] ?? 0, root)
      if (j + 1 < out.length) out[j] = (out[j] ?? 0) ^ (out[j + 1] ?? 0)
    }
    root = gfMultiply(root, 0x02)
  }
  return out
}

/**
 * The Reed-Solomon error correction codewords for a block of data.
 * The degree is how many codewords.
 */
export function reedSolomon(data: number[], degree: number): number[] {
  const divisor = rsDivisor(degree)
  const out: number[] = new Array<number>(degree).fill(0)
  for (const b of data) {
    const factor = b ^ (out.shift() as number)
    out.push(0)
    divisor.forEach((coef, i) => {
      out[i] = (out[i] ?? 0) ^ gfMultiply(coef, factor)
    })
  }
  return out
}

/**
 * The 15 format bits: the level, the mask, their BCH code, and the fixed
 * mask the standard lays over them. The mask is 0 to 7.
 */
export function formatBits(mask: number) {
  const data = (LEVEL_M << 3) | mask
  let rem = data
  for (let i = 0; i < 10; i += 1) rem = (rem << 1) ^ ((rem >>> 9) * 0x537)
  return ((data << 10) | rem) ^ 0x5412
}

/**
 * The 18 version bits, from version 7 on: the version and its BCH code.
 */
export function versionBits(version: number) {
  let rem = version
  for (let i = 0; i < 12; i += 1) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25)
  return (version << 12) | rem
}

/** The rows and columns an alignment pattern is centred on. */
export function alignmentPositions(version: number) {
  if (version === 1) return []
  const count = Math.floor(version / 7) + 2
  const step = Math.floor((version * 8 + count * 3 + 5) / (count * 4 - 4)) * 2
  const out = [6]
  for (let pos = version * 4 + 17 - 7; out.length < count; pos -= step) out.splice(1, 0, pos)
  return out
}

/** The text as the data codewords of a version: mode, count, bytes, terminator and pad. */
function encodeData(bytes: number[], version: number) {
  const bits: number[] = []
  const put = (value: number, length: number) => {
    for (let i = length - 1; i >= 0; i -= 1) bits.push((value >>> i) & 1)
  }
  put(0b0100, 4)
  put(bytes.length, version < 10 ? 8 : 16)
  for (const b of bytes) put(b, 8)
  const room = dataCodewords(version) * 8
  put(0, Math.min(4, room - bits.length))
  put(0, (8 - (bits.length % 8)) % 8)
  const out = []
  for (let i = 0; i < bits.length; i += 8) {
    out.push(bits.slice(i, i + 8).reduce((acc, bit) => (acc << 1) | bit, 0))
  }
  for (let pad = 0xec; out.length < room / 8; pad ^= 0xec ^ 0x11) out.push(pad)
  return out
}

/** The data split into blocks, each with its error correction, interleaved. */
function interleave(data: number[], version: number) {
  const blocks = entry(BLOCKS, version)
  const ecc = entry(ECC_PER_BLOCK, version)
  const raw = Math.floor(rawModules(version) / 8)
  const short = blocks - (raw % blocks)
  const shortLen = Math.floor(raw / blocks)
  const all: number[][] = []
  for (let i = 0, k = 0; i < blocks; i += 1) {
    const block = data.slice(k, k + shortLen - ecc + (i < short ? 0 : 1))
    k += block.length
    const code = reedSolomon(block, ecc)
    if (i < short) block.push(0)
    all.push(block.concat(code))
  }
  const out: number[] = []
  for (let i = 0; i < (all[0]?.length ?? 0); i += 1) {
    all.forEach((block, j) => {
      // The short blocks' placeholder byte is not sent.
      if (i !== shortLen - ecc || j >= short) out.push(block[i] ?? 0)
    })
  }
  return out
}

const MASKS: Array<(x: number, y: number) => boolean> = [
  (x, y) => (x + y) % 2 === 0,
  (_x, y) => y % 2 === 0,
  (x) => x % 3 === 0,
  (x, y) => (x + y) % 3 === 0,
  (x, y) => (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0,
  (x, y) => ((x * y) % 2) + ((x * y) % 3) === 0,
  (x, y) => (((x * y) % 2) + ((x * y) % 3)) % 2 === 0,
  (x, y) => (((x + y) % 2) + ((x * y) % 3)) % 2 === 0,
]

/** A grid being drawn: which modules are dark, and which the patterns own. */
class Grid {
  version: number
  size: number
  dark: boolean[][]
  fixed: boolean[][]

  constructor(version: number) {
    this.version = version
    this.size = version * 4 + 17
    this.dark = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false))
    this.fixed = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false))
  }

  set(x: number, y: number, dark: boolean) {
    this.put(x, y, dark)
    const row = this.fixed[y]
    if (row) row[x] = true
  }

  put(x: number, y: number, dark: boolean) {
    const row = this.dark[y]
    if (row) row[x] = dark
  }

  isDark(x: number, y: number) {
    return this.dark[y]?.[x] ?? false
  }

  isFixed(x: number, y: number) {
    return this.fixed[y]?.[x] ?? false
  }

  drawPatterns() {
    const { size, version } = this
    for (let i = 0; i < size; i += 1) {
      this.set(6, i, i % 2 === 0)
      this.set(i, 6, i % 2 === 0)
    }
    const finders: Array<[number, number]> = [
      [3, 3],
      [size - 4, 3],
      [3, size - 4],
    ]
    for (const [cx, cy] of finders) {
      for (let dy = -4; dy <= 4; dy += 1) {
        for (let dx = -4; dx <= 4; dx += 1) {
          const x = cx + dx
          const y = cy + dy
          const dist = Math.max(Math.abs(dx), Math.abs(dy))
          if (x >= 0 && x < size && y >= 0 && y < size) this.set(x, y, dist !== 2 && dist !== 4)
        }
      }
    }
    const pos = alignmentPositions(version)
    const last = pos.length - 1
    pos.forEach((cy, i) => {
      pos.forEach((cx, j) => {
        // Where a finder pattern sits there is no alignment pattern.
        if ((i === 0 && j === 0) || (i === 0 && j === last) || (i === last && j === 0)) return
        for (let dy = -2; dy <= 2; dy += 1) {
          for (let dx = -2; dx <= 2; dx += 1) {
            this.set(cx + dx, cy + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1)
          }
        }
      })
    })
    this.drawFormat(0)
    if (version >= 7) {
      const bits = versionBits(version)
      for (let i = 0; i < 18; i += 1) {
        const dark = ((bits >>> i) & 1) === 1
        const a = size - 11 + (i % 3)
        const b = Math.floor(i / 3)
        this.set(a, b, dark)
        this.set(b, a, dark)
      }
    }
  }

  /** Both copies of the format bits, and the one module that is always dark. */
  drawFormat(mask: number) {
    const { size } = this
    const bits = formatBits(mask)
    const bit = (i: number) => ((bits >>> i) & 1) === 1
    for (let i = 0; i <= 5; i += 1) this.set(8, i, bit(i))
    this.set(8, 7, bit(6))
    this.set(8, 8, bit(7))
    this.set(7, 8, bit(8))
    for (let i = 9; i < 15; i += 1) this.set(14 - i, 8, bit(i))
    for (let i = 0; i < 8; i += 1) this.set(size - 1 - i, 8, bit(i))
    for (let i = 8; i < 15; i += 1) this.set(8, size - 15 + i, bit(i))
    this.set(8, size - 8, true)
  }

  /** The codewords, two columns at a time, up and down, right to left. */
  drawCodewords(codewords: number[]) {
    const { size } = this
    let i = 0
    for (let right = size - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5
      for (let vert = 0; vert < size; vert += 1) {
        for (let j = 0; j < 2; j += 1) {
          const x = right - j
          const up = ((right + 1) & 2) === 0
          const y = up ? size - 1 - vert : vert
          if (!this.isFixed(x, y) && i < codewords.length * 8) {
            this.put(x, y, (((codewords[i >>> 3] ?? 0) >>> (7 - (i & 7))) & 1) === 1)
            i += 1
          }
        }
      }
    }
  }

  applyMask(mask: number) {
    const fn = MASKS[mask]
    if (!fn) throw new RangeError(`no mask ${mask}`)
    for (let y = 0; y < this.size; y += 1) {
      for (let x = 0; x < this.size; x += 1) {
        if (!this.isFixed(x, y) && fn(x, y)) this.put(x, y, !this.isDark(x, y))
      }
    }
  }

  /** The standard's four penalties: runs, blocks, finder look-alikes, balance. */
  penalty() {
    const { size } = this
    const cell = (x: number, y: number) => this.isDark(x, y)
    let score = 0
    const line = (get: (i: number) => boolean) => {
      let color = false
      let run = 0
      const history = [0, 0, 0, 0, 0, 0, 0]
      // The first run of a line takes in the light border before it.
      const push = (length: number) => {
        const first = history[0] === 0
        history.pop()
        history.unshift(first ? length + size : length)
      }
      const finders = () => {
        const [h0 = 0, n = 0, h2 = 0, h3 = 0, h4 = 0, h5 = 0, h6 = 0] = history
        const core = n > 0 && h2 === n && h3 === n * 3 && h4 === n && h5 === n
        return (core && h0 >= n * 4 && h6 >= n ? 1 : 0) + (core && h6 >= n * 4 && h0 >= n ? 1 : 0)
      }
      for (let i = 0; i < size; i += 1) {
        if (get(i) === color) {
          run += 1
          if (run === 5) score += 3
          else if (run > 5) score += 1
        } else {
          push(run)
          if (!color) score += finders() * 40
          color = get(i)
          run = 1
        }
      }
      if (color) {
        push(run)
        run = 0
      }
      push(run + size)
      score += finders() * 40
    }
    for (let y = 0; y < size; y += 1) line((x) => cell(x, y))
    for (let x = 0; x < size; x += 1) line((y) => cell(x, y))
    for (let y = 0; y < size - 1; y += 1) {
      for (let x = 0; x < size - 1; x += 1) {
        const c = cell(x, y)
        if (c === cell(x + 1, y) && c === cell(x, y + 1) && c === cell(x + 1, y + 1)) score += 3
      }
    }
    const darkCount = this.dark.reduce((sum, row) => sum + row.filter(Boolean).length, 0)
    const total = size * size
    score += (Math.ceil(Math.abs(darkCount * 20 - total * 10) / total) - 1) * 10
    return score
  }
}

export interface QrCode {
  version: number
  size: number
  mask: number
  modules: boolean[][]
}

/**
 * Encodes text as a QR code. modules[y][x] is true where the module is dark.
 */
export function encode(text: string): QrCode {
  const bytes = [...new TextEncoder().encode(text)]
  let version = 1
  while (version <= 40 && capacity(version) < bytes.length) version += 1
  if (version > 40) throw new RangeError(`${bytes.length} bytes do not fit a QR code`)
  const codewords = interleave(encodeData(bytes, version), version)
  const candidates = Array.from({ length: 8 }, (_, mask) => {
    const grid = new Grid(version)
    grid.drawPatterns()
    grid.drawCodewords(codewords)
    grid.applyMask(mask)
    grid.drawFormat(mask)
    return { score: grid.penalty(), mask, grid }
  })
  const best = candidates.reduce((a, b) => (b.score < a.score ? b : a))
  return {
    version,
    size: best.grid.size,
    mask: best.mask,
    modules: best.grid.dark,
  }
}

/**
 * The dark modules as one SVG path, a unit square each, for a viewBox of
 * the code's size plus the four-module quiet zone the standard asks for.
 */
export function svgPath(code: Pick<QrCode, 'size' | 'modules'>): string {
  const parts: string[] = []
  code.modules.forEach((row, y) => {
    row.forEach((dark, x) => {
      if (dark) parts.push(`M${x + 4} ${y + 4}h1v1h-1z`)
    })
  })
  return parts.join('')
}
