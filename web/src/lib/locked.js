import { computed, inject, provide } from 'vue'

const LOCKED = Symbol('locked')

/**
 * Marks what a card or a dialog holds as settings the account may not
 * change. They show disabled, and a fold inside stays open, since its
 * trigger is disabled with the rest.
 *
 * @param {() => boolean} locked
 */
export function provideLocked(locked) {
  provide(LOCKED, computed(locked))
}

/** Whether the nearest card or dialog is locked. */
export function useLocked() {
  return inject(
    LOCKED,
    computed(() => false),
  )
}
