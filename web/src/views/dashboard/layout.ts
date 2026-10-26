/**
 * The layout of the dashboard: the order of the grid's cards and which of
 * them this browser hides. It is this browser's only, kept apart from the
 * shape's key, which the dashboard rewrites on every overview read. Names
 * only, never an address or a name of a person or a place.
 */

export const LAYOUT_KEY = 'ostiole.dashboard.layout'

/**
 * The grid's cards, by the data-card name each carries, with the title its
 * header shows. The order they are listed in is the order the page starts
 * with.
 */
export const CARDS = Object.freeze([
  { name: 'system', title: 'System' },
  { name: 'gateways', title: 'Gateways' },
  { name: 'traffic', title: 'Traffic' },
  { name: 'rules', title: 'Busiest rules' },
  { name: 'blocks', title: 'Recent blocks' },
  { name: 'services', title: 'Services' },
  { name: 'leases', title: 'Recent leases' },
  { name: 'wireless', title: 'Wireless' },
  { name: 'blocking', title: 'DNS blocking' },
  { name: 'router', title: 'Router' },
])

/** The order the page starts with, and the one Restore brings back. */
export const DEFAULT_ORDER = Object.freeze(CARDS.map((c) => c.name))

/**
 * A stored layout made sensible. Names this build does not know are
 * dropped, a known name missing from the order is appended where the
 * default puts it, and a hidden name must be one the order holds.
 *
 * @param {unknown} raw
 */
export function normalizeLayout(raw) {
  if (!raw || typeof raw !== 'object') return firstLayout()
  const stored = Array.isArray(raw.order) ? raw.order : []
  const order = []
  for (const name of stored) {
    if (DEFAULT_ORDER.includes(name) && !order.includes(name)) order.push(name)
  }
  for (const name of DEFAULT_ORDER) {
    if (!order.includes(name)) order.push(name)
  }
  const storedHidden = Array.isArray(raw.hidden) ? raw.hidden : []
  const hidden = []
  for (const name of storedHidden) {
    if (order.includes(name) && !hidden.includes(name)) hidden.push(name)
  }
  return { order, hidden }
}

/** A fresh copy of the first-visit layout. */
export function firstLayout() {
  return { order: [...DEFAULT_ORDER], hidden: [] }
}

/** A layout that is not the one the page starts with. */
export function isCustomized(layout) {
  if (layout.hidden.length) return true
  return layout.order.some((name, i) => name !== DEFAULT_ORDER[i])
}

/**
 * The layout this browser kept, or the first-visit one. A browser that
 * refuses storage is no worse off.
 */
export function loadLayout() {
  let raw = null
  try {
    raw = JSON.parse(localStorage.getItem(LAYOUT_KEY) ?? 'null')
  } catch {
    /* unreadable, so start over */
  }
  return normalizeLayout(raw)
}

/**
 * Keeps a layout for the next visit. A browser that refuses storage is no
 * worse off.
 *
 * @param {{order: string[], hidden: string[]}} layout
 */
export function saveLayout(layout) {
  try {
    const next = JSON.stringify(layout)
    if (localStorage.getItem(LAYOUT_KEY) !== next) localStorage.setItem(LAYOUT_KEY, next)
  } catch {
    /* storage full or blocked */
  }
}
