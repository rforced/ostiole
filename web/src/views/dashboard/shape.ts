/**
 * The shape of the dashboard: which of the cards that come and go it
 * showed, how many rows each list had, and how tall each block came out.
 * Until the overview is in, the placeholders take the last shape this
 * browser saw, so the data lands where they are instead of reshuffling the
 * page. Counts and heights only, never an address or a name.
 */

/** What an overview has to hold for its shape to be read: rows are only counted. */
export interface ShapeSource {
  warnings?: unknown[] | null
  interfaces?: { name: string; external?: boolean; present?: boolean }[] | null
  topRules?: unknown[] | null
  services?: unknown[] | null
  gateways?: unknown[] | null
  unwatchedGateways?: unknown[] | null
  recentBlocks?: unknown[] | null
  recentLeases?: unknown[] | null
  dhcp?: { enabled?: boolean }
  wireless?: { clients?: unknown[] | null }
  blocking?: { enabled?: boolean }
}

export interface Shape {
  warnings: number
  interfaces: number
  rules: number
  services: number
  cards: Record<string, number>
  heights?: Record<string, number>
}

export const SHAPE_KEY = 'ostiole.dashboard.shape'

/** What a first visit draws: the cards every router has, at their old sizes. */
export const FIRST_SHAPE: Readonly<Shape> = Object.freeze({
  warnings: 0,
  interfaces: 3,
  rules: 3,
  services: 3,
  cards: Object.freeze<Record<string, number>>({}),
  heights: Object.freeze<Record<string, number>>({}),
})

/** The cards that only exist when the overview has something for them. */
export const OPTIONAL = ['gateways', 'traffic', 'blocks', 'leases', 'wireless', 'blocking']

/** The blocks of the page, by the data-card name each carries. */
const BLOCKS = ['warnings', 'interfaces', 'system', 'rules', 'services', 'router', ...OPTIONAL]

/** No placeholder list runs longer than this, whatever was stored. */
const MOST = 20
/** No placeholder is held taller than this, in pixels. */
const TALLEST = 4000

/**
 * The part of the shape an overview decides: which optional cards show,
 * with the rows of each, and the rows of the lists every router has.
 */
export function shapeOf(ov: ShapeSource): Shape {
  const cards: Record<string, number> = {}
  const gateways = (ov.gateways?.length ?? 0) + (ov.unwatchedGateways?.length ?? 0)
  if (gateways) cards.gateways = gateways
  const external = externalLinks(ov).length
  if (external) cards.traffic = external
  if (ov.recentBlocks) cards.blocks = ov.recentBlocks.length
  if (ov.dhcp?.enabled) cards.leases = ov.recentLeases?.length ?? 0
  if (ov.wireless) cards.wireless = ov.wireless.clients?.length ?? 0
  if (ov.blocking?.enabled) cards.blocking = 0
  return {
    warnings: ov.warnings?.length ?? 0,
    interfaces: ov.interfaces?.length ?? 0,
    rules: ov.topRules?.length ?? 0,
    services: ov.services?.length ?? 0,
    cards,
  }
}

/** The external links present, which the Traffic card draws. */
export function externalLinks(ov: ShapeSource | null): string[] {
  return (ov?.interfaces ?? []).filter((l) => l.external && l.present).map((l) => l.name)
}

/**
 * How tall each block of the page came out. A card in a row of two is as
 * tall as the row, which is the room its placeholder should hold.
 */
export function heightsIn(root: Element) {
  const out: Record<string, number> = {}
  for (const el of root.querySelectorAll<HTMLElement>('[data-card]')) {
    const h = el.offsetHeight
    const card = el.dataset.card
    if (h && card !== undefined) out[card] = h
  }
  return out
}

/** A stored count, or the fallback when it is not a sensible one. */
function count(v: unknown, fallback: number) {
  return typeof v === 'number' && Number.isInteger(v) && v >= 0 ? Math.min(v, MOST) : fallback
}

/** An object from storage, or an empty one for anything else. */
function object(v: unknown): Record<string, unknown> {
  return v && typeof v === 'object' ? { ...v } : {}
}

/** The last shape this browser saw, or the first-visit one. */
export function rememberedShape(): Readonly<Shape> {
  let raw: Record<string, unknown> | null = null
  try {
    raw = JSON.parse(localStorage.getItem(SHAPE_KEY) ?? 'null')
  } catch {
    /* unreadable, so start over */
  }
  if (!raw || typeof raw !== 'object') return FIRST_SHAPE
  const stored = object(raw.cards)
  const cards: Record<string, number> = {}
  for (const name of OPTIONAL) {
    if (Object.hasOwn(stored, name)) cards[name] = count(stored[name], 0)
  }
  const held = object(raw.heights)
  const heights: Record<string, number> = {}
  for (const name of BLOCKS) {
    const h = held[name]
    if (typeof h === 'number' && Number.isInteger(h) && h > 0 && h <= TALLEST) heights[name] = h
  }
  return {
    warnings: count(raw.warnings, FIRST_SHAPE.warnings),
    interfaces: count(raw.interfaces, FIRST_SHAPE.interfaces),
    rules: count(raw.rules, FIRST_SHAPE.rules),
    services: count(raw.services, FIRST_SHAPE.services),
    cards,
    heights,
  }
}

/**
 * Keeps a shape for the next visit. A browser that refuses storage is no
 * worse off.
 */
export function rememberShape(shape: Shape) {
  try {
    const next = JSON.stringify(shape)
    if (localStorage.getItem(SHAPE_KEY) !== next) localStorage.setItem(SHAPE_KEY, next)
  } catch {
    /* storage full or blocked */
  }
}
