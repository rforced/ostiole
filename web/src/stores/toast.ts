import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Toast {
  id: number
  message: string
  action: { label: string; run: () => void } | null
}

const MAX_SHOWN = 4

/**
 * Short-lived notices in the corner: what just happened, and a way back
 * when there is one. Nothing here carries role="status"; the apply bar
 * owns that.
 */
export const useToastStore = defineStore('toast', () => {
  const toasts = ref<Toast[]>([])
  let seq = 0

  /** The timeout is the milliseconds before it goes by itself, 0 to stay. Returns the toast id. */
  function show(
    message: string,
    { action = null, timeout = 5000 }: { action?: Toast['action']; timeout?: number } = {},
  ): number {
    const id = ++seq
    toasts.value = [...toasts.value.slice(-(MAX_SHOWN - 1)), { id, message, action }]
    if (timeout) window.setTimeout(() => dismiss(id), timeout)
    return id
  }

  function dismiss(id: number) {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }

  /** Runs a toast's action and takes the toast away. */
  function act(id: number) {
    const t = toasts.value.find((x) => x.id === id)
    dismiss(id)
    t?.action?.run()
  }

  function clear() {
    toasts.value = []
  }

  return { toasts, show, dismiss, act, clear }
})
