import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface ConfirmRequest {
  /** The dialog title, e.g. "Delete rule 12?" */
  question: string
  /** A line under it, or empty. */
  description: string
  /** The button that says yes. */
  confirmLabel: string
  /** What goes with it, shown as a list. */
  dependents: string[]
  /** Heading over that list. */
  dependentsLabel: string
  /** A name to type back before yes is enabled. */
  typed: string
  /** Red button. */
  danger: boolean
}

/**
 * One confirmation dialog for the whole app, mounted once in App.vue.
 * `ask` resolves true when the admin confirms and false when they close
 * the dialog any other way.
 */
export const useConfirmStore = defineStore('confirm', () => {
  const request = ref<ConfirmRequest | null>(null)
  let resolve: ((answer: boolean) => void) | null = null

  function ask({
    question,
    description = '',
    confirmLabel = 'Delete',
    dependents = [],
    dependentsLabel = 'Also deleted',
    typed = '',
    danger = true,
  }: Partial<ConfirmRequest> & { question: string }): Promise<boolean> {
    // A second question while one is open answers the first with no.
    settle(false)
    request.value = {
      question,
      description,
      confirmLabel,
      dependents,
      dependentsLabel,
      typed,
      danger,
    }
    return new Promise<boolean>((r) => {
      resolve = r
    })
  }

  function settle(answer: boolean) {
    const r = resolve
    resolve = null
    request.value = null
    if (r) r(answer)
  }

  return { request, ask, settle }
})
