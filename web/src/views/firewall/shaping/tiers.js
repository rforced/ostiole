/**
 * The four priorities, in the order they beat each other. The names are the
 * router's; these are the labels and the one line each needs to explain
 * what choosing it costs somebody else.
 */
export const TIERS = [
  { value: 'bulk', label: 'Bulk', hint: 'yields to everything else' },
  { value: 'normal', label: 'Normal', hint: 'ordinary, even if the device asks for better' },
  { value: 'high', label: 'High', hint: 'ahead of ordinary traffic' },
  { value: 'realtime', label: 'Realtime', hint: 'goes first' },
]

/**
 * Setting no priority is not the same as setting Normal, and the two are
 * next to each other in the same list, so the difference has to be in the
 * words. Nothing is written on the packet and the queue reads whatever
 * marking the device asked for: a call that marks itself urgent is
 * treated as urgent. Normal overrules that marking; this defers to it.
 *
 * It is one string because both dialogs offer it and they must not drift.
 */
export const UNSET_LABEL = "Not set: the device's own marking decides"

/** A tier as its option in a priority list reads. */
export const tierOption = (t) => `${t.label}: ${t.hint}`

/** The line under either priority field, for the same reason. */
export const PRIORITY_HINT =
  'Unset leaves it to what the device asks for. Anything else overrides that.'

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
