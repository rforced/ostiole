/**
 * The shape of the dashboard: which of the cards that come and go it
 * showed, how many rows each list had, and how tall each block came out.
 * Until the overview is in, the placeholders take the last shape this
 * browser saw, so the data lands where they are instead of reshuffling the
 * page. Counts and heights only, never an address or a name.
 */

export const SHAPE_KEY = 'ostiole.dashboard.shape'

/** What a first visit draws: the cards every router has, at their old sizes. */
export const FIRST_SHAPE = Object.freeze({
  warnings: 0,
  interfaces: 3,
  rules: 3,
  services: 3,
  cards: Object.freeze({}),
  heights: Object.freeze({}),
})

/** The cards that only exist when the overview has something for them. */
const OPTIONAL = ['gateways', 'blocks', 'leases', 'wireless', 'blocking']

/** The blocks of the page, by the data-card name each carries. */
const BLOCKS = ['warnings', 'interfaces', 'system', 'rules', 'services', 'router', ...OPTIONAL]

/** No placeholder list runs longer than this, whatever was stored. */
const MOST = 20
/** No placeholder is held taller than this, in pixels. */
const TALLEST = 4000

/**
 * The part of the shape an overview decides: which optional cards show,
 * with the rows of each, and the rows of the lists every router has.
 *
 * @param {object} ov a reply from GET /overview
 */
export function shapeOf(ov) {
  /** @type {Record<string, number>} */
  const cards = {}
  const gateways = (ov.gateways?.length ?? 0) + (ov.unwatchedGateways?.length ?? 0)
  if (gateways) cards.gateways = gateways
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

/**
 * How tall each block of the page came out. A card in a row of two is as
 * tall as the row, which is the room its placeholder should hold.
 *
 * @param {Element} root
 */
export function heightsIn(root) {
  /** @type {Record<string, number>} */
  const out = {}
  for (const el of root.querySelectorAll('[data-card]')) {
    const h = /** @type {HTMLElement} */ (el).offsetHeight
    if (h) out[/** @type {HTMLElement} */ (el).dataset.card] = h
  }
  return out
}

/** A stored count, or the fallback when it is not a sensible one. */
function count(v, fallback) {
  return Number.isInteger(v) && v >= 0 ? Math.min(v, MOST) : fallback
}

/** An object from storage, or an empty one for anything else. */
function object(v) {
  return v && typeof v === 'object' ? v : {}
}

/** The last shape this browser saw, or the first-visit one. */
export function rememberedShape() {
  let raw = null
  try {
    raw = JSON.parse(localStorage.getItem(SHAPE_KEY) ?? 'null')
  } catch {
    /* unreadable, so start over */
  }
  if (!raw || typeof raw !== 'object') return FIRST_SHAPE
  const stored = object(raw.cards)
  /** @type {Record<string, number>} */
  const cards = {}
  for (const name of OPTIONAL) {
    if (Object.hasOwn(stored, name)) cards[name] = count(stored[name], 0)
  }
  const held = object(raw.heights)
  /** @type {Record<string, number>} */
  const heights = {}
  for (const name of BLOCKS) {
    const h = held[name]
    if (Number.isInteger(h) && h > 0 && h <= TALLEST) heights[name] = h
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
 *
 * @param {object} shape
 */
export function rememberShape(shape) {
  try {
    const next = JSON.stringify(shape)
    if (localStorage.getItem(SHAPE_KEY) !== next) localStorage.setItem(SHAPE_KEY, next)
  } catch {
    /* storage full or blocked */
  }
}
