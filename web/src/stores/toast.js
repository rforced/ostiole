import { defineStore } from 'pinia'
import { ref } from 'vue'

/**
 * @typedef {object} Toast
 * @property {number} id
 * @property {string} message
 * @property {{label: string, run: () => void} | null} action
 */

const MAX_SHOWN = 4

/**
 * Short-lived notices in the corner: what just happened, and a way back
 * when there is one. Nothing here carries role="status"; the apply bar
 * owns that.
 */
export const useToastStore = defineStore('toast', () => {
  /** @type {import('vue').Ref<Toast[]>} */
  const toasts = ref([])
  let seq = 0

  /**
   * @param {string} message
   * @param {{action?: Toast['action'], timeout?: number}} [opts]
   *   timeout: milliseconds before it goes by itself, 0 to stay
   * @returns {number} the toast id
   */
  function show(message, { action = null, timeout = 5000 } = {}) {
    const id = ++seq
    toasts.value = [...toasts.value.slice(-(MAX_SHOWN - 1)), { id, message, action }]
    if (timeout) window.setTimeout(() => dismiss(id), timeout)
    return id
  }

  function dismiss(id) {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }

  /** Runs a toast's action and takes the toast away. */
  function act(id) {
    const t = toasts.value.find((x) => x.id === id)
    dismiss(id)
    t?.action?.run()
  }

  function clear() {
    toasts.value = []
  }

  return { toasts, show, dismiss, act, clear }
})
