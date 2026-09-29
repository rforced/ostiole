import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { env } from 'node:process'

import { describe, expect, it } from 'vitest'

import {
  alignmentPositions,
  capacity,
  dataCodewords,
  encode,
  formatBits,
  reedSolomon,
  svgPath,
  versionBits,
} from '@/lib/qr'

describe('qr tables', () => {
  // The two worked examples every account of the standard gives, both
  // version 1-M: "HELLO WORLD" in alphanumeric mode, and ISO 18004's own
  // "01234567" in numeric mode.
  it('computes the Reed-Solomon codewords of the worked examples', () => {
    expect(
      reedSolomon([32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17], 10),
    ).toEqual([196, 35, 39, 119, 235, 215, 231, 226, 93, 23])
    expect(
      reedSolomon([16, 32, 12, 86, 97, 128, 236, 17, 236, 17, 236, 17, 236, 17, 236, 17], 10),
    ).toEqual([165, 36, 212, 193, 237, 54, 199, 135, 44, 85])
  })

  it('matches the standard format information for level M', () => {
    expect([0, 1, 2, 3, 4, 5, 6, 7].map(formatBits)).toEqual([
      0b101010000010010, 0b101000100100101, 0b101111001111100, 0b101101101001011, 0b100010111111001,
      0b100000011001110, 0b100111110010111, 0b100101010100000,
    ])
  })

  it('matches the standard version information', () => {
    expect(versionBits(7)).toBe(0x07c94)
    expect(versionBits(8)).toBe(0x085bc)
    expect(versionBits(40)).toBe(0x28c69)
  })

  it('places alignment patterns where the standard does', () => {
    expect(alignmentPositions(1)).toEqual([])
    expect(alignmentPositions(2)).toEqual([6, 18])
    expect(alignmentPositions(7)).toEqual([6, 22, 38])
    expect(alignmentPositions(14)).toEqual([6, 26, 46, 66])
    expect(alignmentPositions(32)).toEqual([6, 34, 60, 86, 112, 138])
    expect(alignmentPositions(40)).toEqual([6, 30, 58, 86, 114, 142, 170])
  })

  it('holds what the standard says each version holds at level M', () => {
    expect([1, 5, 10, 40].map(dataCodewords)).toEqual([16, 86, 216, 2334])
    expect([1, 2, 3, 10, 40].map(capacity)).toEqual([14, 26, 42, 213, 2331])
  })

  it('takes the smallest version that fits', () => {
    expect(encode('x'.repeat(14)).version).toBe(1)
    expect(encode('x'.repeat(15)).version).toBe(2)
    expect(encode('x'.repeat(2331)).version).toBe(40)
    expect(() => encode('x'.repeat(2332))).toThrow(RangeError)
  })

  it('draws one unit square per dark module, inside the quiet zone', () => {
    const code = encode('hi')
    const dark = code.modules.flat().filter(Boolean).length
    expect(svgPath(code).match(/M/g)).toHaveLength(dark)
    expect(svgPath(code).startsWith('M4 4h1v1h-1z')).toBe(true)
  })
})

/** The code as a plain PBM, four pixels a module, with its quiet zone. */
function pbm(code) {
  const scale = 4
  const width = (code.size + 8) * scale
  const rows = []
  for (let py = 0; py < width; py += 1) {
    const y = Math.floor(py / scale) - 4
    let row = ''
    for (let px = 0; px < width; px += 1) {
      const x = Math.floor(px / scale) - 4
      row += code.modules[y]?.[x] ? '1' : '0'
    }
    rows.push(row)
  }
  return `P1\n${width} ${width}\n${rows.join('\n')}\n`
}

const haveZbar = spawnSync('zbarimg', ['--version']).status === 0

// A decoder written by someone else reads back what the encoder wrote, at
// sizes that cross versions and block layouts. zbarimg is in CI, where
// TESTENV_REQUIRE makes its absence a failure; locally it runs in an
// ubuntu:24.04 container, since installing it here would change the
// workstation.
describe.skipIf(!haveZbar && env.TESTENV_REQUIRE !== '1')('qr round trip', () => {
  it.each([1, 14, 15, 100, 213, 214, 300, 500, 1000, 1500, 2331])(
    'reads back %i bytes',
    (length) => {
      const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/= \n[]'
      let text = ''
      for (let i = 0; i < length; i += 1) text += alphabet[(i * 7 + length) % alphabet.length]
      const dir = mkdtempSync(join(tmpdir(), 'qr-'))
      try {
        const file = join(dir, 'code.pbm')
        writeFileSync(file, pbm(encode(text)))
        const out = spawnSync('zbarimg', ['--raw', '-q', file], { encoding: 'utf8' })
        expect(out.status).toBe(0)
        expect(out.stdout.replace(/\n$/, '')).toBe(text)
      } finally {
        rmSync(dir, { recursive: true, force: true })
      }
    },
  )
})
