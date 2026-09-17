/**
 * The four priorities, in the order they beat each other. The names are the
 * router's; these are the labels and the one line each needs to explain
 * what choosing it costs somebody else.
 */
export const TIERS = [
  { value: 'bulk', label: 'Bulk', hint: 'Yields to everything else.' },
  { value: 'normal', label: 'Normal', hint: 'Where unmarked traffic goes.' },
  { value: 'high', label: 'High', hint: 'Ahead of ordinary traffic.' },
  { value: 'realtime', label: 'Realtime', hint: 'Goes first.' },
]

/** The label for a tier the router named, or the raw value if it is new. */
export function tierLabel(value) {
  return TIERS.find((t) => t.value === value)?.label ?? value
}

/**
 * How a tier badge reads. Only the two ends are coloured: the point of the
 * badge is to show that a rule is not where everything else is, and
 * colouring all four turns the table into a rainbow nobody reads.
 */
export function tierBadge(value) {
  if (value === 'realtime' || value === 'high') return 'badge badge-ok'
  if (value === 'bulk') return 'badge badge-warn'
  return 'badge'
}
