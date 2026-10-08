import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import colors from 'tailwindcss/colors'
import { describe, expect, it } from 'vitest'

// Vitest empties CSS, a ?raw import included.
const css = readFileSync(join(import.meta.dirname, '../main.css'), 'utf8')

// WCAG AAA: text holds 7:1 against whatever it sits on.
const AAA = 7

// WCAG 1.4.11: what shows where a control is, or what state it is in, and
// the parts of a graphic, hold 3:1.
const NON_TEXT = 3

const TONES = ['ok', 'warn', 'bad', 'info']

/** sRGB channels from 0 to 1, clipped to the gamut WCAG measures in. */
function oklch(l, c, h) {
  const a = c * Math.cos((h * Math.PI) / 180)
  const b = c * Math.sin((h * Math.PI) / 180)
  const [x, y, z] = [
    l + 0.3963377774 * a + 0.2158037573 * b,
    l - 0.1055613458 * a - 0.0638541728 * b,
    l - 0.0894841775 * a - 1.291485548 * b,
  ].map((v) => v ** 3)
  return [
    4.0767416621 * x - 3.3077115913 * y + 0.2309699292 * z,
    -1.2684380046 * x + 2.6097574011 * y - 0.3413193965 * z,
    -0.0041960863 * x - 0.7034186147 * y + 1.707614701 * z,
  ].map((v) => {
    const s = v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055
    return Math.min(1, Math.max(0, s))
  })
}

/** A token's value as a colour and its alpha. */
function parse(value) {
  let m
  if ((m = value.match(/^var\(--color-(\w+)-(\d+)\)$/))) return parse(colors[m[1]][m[2]])
  if ((m = value.match(/^oklch\(([\d.]+)% ([\d.]+) ([\d.]+|none)\)$/)))
    return { rgb: oklch(m[1] / 100, +m[2], m[3] === 'none' ? 0 : +m[3]), alpha: 1 }
  if ((m = value.match(/^color-mix\(in oklab, (.+) (\d+)%, transparent\)$/)))
    return { ...parse(m[1]), alpha: m[2] / 100 }
  if (value === '#fff') return { rgb: [1, 1, 1], alpha: 1 }
  throw new Error(`cannot read ${value}`)
}

/** The tokens one of main.css's two blocks sets. */
function tokens(selector) {
  const start = css.indexOf(`\n${selector} {`)
  const block = css.slice(start, css.indexOf('\n}', start))
  return Object.fromEntries(
    [...block.matchAll(/--([\w-]+): ([^;]+);/g)].map(([, name, value]) => [name, parse(value)]),
  )
}

const over = (top, below) => ({
  rgb: top.rgb.map((v, i) => v * top.alpha + below.rgb[i] * (1 - top.alpha)),
  alpha: 1,
})

function luminance({ rgb }) {
  const [r, g, b] = rgb.map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

/** Every text colour on every ground the views put it on, as [text, ground, ratio]. */
function pairs(t) {
  const on = (name, alpha = 1) => over({ ...t[name], alpha: t[name].alpha * alpha }, t.surface)
  const grounds = {
    page: t.page,
    surface: t.surface,
    'surface-2': on('surface-2'),
    // A table's header, and a rule Ostiole adds.
    'surface-2/60': on('surface-2', 0.6),
    'surface-2/40': on('surface-2', 0.4),
  }
  const soft = Object.fromEntries(TONES.map((k) => [`${k}-soft`, on(`${k}-soft`)]))
  const out = []
  const add = (text, ground, fg, bg) => out.push([text, ground, contrast(over(fg, bg), bg)])
  for (const ink of ['ink', 'ink-2', 'ink-muted'])
    for (const [ground, bg] of Object.entries(grounds)) add(ink, ground, t[ink], bg)
  for (const text of ['accent-text', ...TONES])
    for (const ground of ['page', 'surface', 'surface-2/40'])
      add(text, ground, t[text], grounds[ground])
  // A notice holds links, and the confirm banner says Confirmed in ok.
  for (const [ground, bg] of Object.entries(soft)) add('accent-text', ground, t['accent-text'], bg)
  add('ok', 'warn-soft', t.ok, soft['warn-soft'])
  for (const k of TONES) add(`${k}-ink`, `${k}-soft`, t[`${k}-ink`], soft[`${k}-soft`])
  // Button labels. The Delete button fades to 90% under the pointer.
  for (const fill of ['accent', 'accent-hover']) add('on-fill', fill, t['on-fill'], on(fill))
  add('on-danger', 'danger', t['on-danger'], on('danger'))
  add('on-danger', 'danger at 90%', on('on-danger', 0.9), on('danger', 0.9))
  return out
}

/** Every edge and graphic on the grounds the views put it on, as [what, ground, ratio]. */
function graphics(t) {
  const on = (name, alpha = 1) => over({ ...t[name], alpha: t[name].alpha * alpha }, t.surface)
  const white = { rgb: [1, 1, 1], alpha: 1 }
  const out = []
  const add = (what, ground, fg, bg) => out.push([what, ground, contrast(over(fg, bg), bg)])
  // A field's edge and a switch that is off, wherever a form sits.
  for (const [ground, bg] of Object.entries({
    page: t.page,
    surface: t.surface,
    'surface-2': on('surface-2'),
  }))
    add('edge', ground, t.edge, bg)
  // A meter's fill on its own track, a fifth of its strength, and on the card.
  for (const fill of ['accent', 'warn-fill', 'bad-fill']) {
    add(fill, `${fill}/20`, t[fill], on(fill, 0.2))
    add(fill, 'surface', t[fill], t.surface)
  }
  // A switch's knob: white on its edge while off, on-fill on the accent while on.
  add('knob', 'edge', white, t.edge)
  add('knob', 'accent', t['on-fill'], t.accent)
  // Traffic's lines on the card they are drawn on.
  for (const line of ['chart-down', 'chart-up']) add(line, 'surface', t[line], t.surface)
  // A gateway's strip: up, slow or losing packets, and down, on the card.
  for (const fill of ['ok-fill', 'warn-fill', 'bad-fill']) add(fill, 'surface', t[fill], t.surface)
  return out
}

describe.each([
  ['light', ':root'],
  ['dark', '.dark'],
])('%s', (_, selector) => {
  it.each(pairs(tokens(selector)))('%s on %s', (_text, _ground, ratio) => {
    expect(ratio).toBeGreaterThanOrEqual(AAA)
  })
  it.each(graphics(tokens(selector)))('%s edge on %s', (_what, _ground, ratio) => {
    expect(ratio).toBeGreaterThanOrEqual(NON_TEXT)
  })
})
