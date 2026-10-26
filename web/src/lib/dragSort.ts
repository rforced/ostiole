import { ref } from 'vue'

import { useReorder } from '@/lib/reorder'

/**
 * The index of the centre nearest y.
 */
export function nearest(y: number, centres: number[]) {
  let best = 0
  for (let i = 1; i < centres.length; i++) {
    if (Math.abs(centres[i] - y) < Math.abs(centres[best] - y)) best = i
  }
  return best
}

/**
 * Drags a row of a vertical list to a new place with a mouse, a finger or
 * a pen. The row takes the slot nearest its centre as it goes, and the
 * others slide aside. The slots are measured at the grab, so a row still
 * sliding cannot move them. Pointer only.
 *
 * The list's element children are its rows, in order. The handle's
 * pointerdown calls `grab`; the pointer is captured on the list, which
 * never moves, since a row that Vue moves can lose a capture it holds.
 * @param order the rows' ids, rewritten as a row moves
 * @param drop called when a drag ends with the order changed
 */
export function useDragSort(
  list: import('vue').Ref<HTMLElement | null>,
  order: import('vue').WritableComputedRef<string[]>,
  drop: () => void,
) {
  const { moveClass, reorder } = useReorder()
  /** The id of the row being dragged, empty while none is. */
  const dragging = ref('')
  /** @type {string[]} */
  let before = []
  /** @type {number[]} */
  let centres = []
  let grabbedAt = 0
  let pointer = -1
  /** @type {HTMLElement | null} */
  let captured = null

  /** @param {PointerEvent} event */
  function grab(event, id) {
    const el = list.value
    if (dragging.value || !el || (event.pointerType === 'mouse' && event.button !== 0)) return
    // A mouse would start selecting the titles it passes.
    event.preventDefault()
    before = [...order.value]
    centres = [...el.children].map((row) => {
      const r = row.getBoundingClientRect()
      return r.top + r.height / 2
    })
    grabbedAt = event.clientY - centres[before.indexOf(id)]
    pointer = event.pointerId
    dragging.value = id
    captured = el
    el.setPointerCapture(pointer)
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', release)
    el.addEventListener('pointercancel', cancel)
  }

  /** @param {PointerEvent} event */
  function move(event) {
    if (event.pointerId !== pointer) return
    const id = dragging.value
    const to = nearest(event.clientY - grabbedAt, centres)
    if (to === order.value.indexOf(id)) return
    reorder(() => {
      const next = order.value.filter((n) => n !== id)
      next.splice(to, 0, id)
      order.value = next
    })
  }

  const moved = () => order.value.some((id, i) => id !== before[i])

  /** @param {PointerEvent} event */
  function release(event) {
    if (event.pointerId !== pointer) return
    const changed = moved()
    stop()
    if (changed) drop()
  }

  /** @param {PointerEvent} event */
  function cancel(event) {
    if (event.pointerId !== pointer) return
    if (moved()) reorder(() => (order.value = before))
    stop()
  }

  /** Ends a drag where it stands, saving nothing. */
  function stop() {
    if (captured) {
      captured.removeEventListener('pointermove', move)
      captured.removeEventListener('pointerup', release)
      captured.removeEventListener('pointercancel', cancel)
      if (captured.hasPointerCapture(pointer)) captured.releasePointerCapture(pointer)
    }
    captured = null
    pointer = -1
    dragging.value = ''
  }

  return { dragging, moveClass, grab, stop }
}
