import { defineStore } from 'pinia'
import { ref } from 'vue'

/**
 * @typedef {object} ConfirmRequest
 * @property {string} question the dialog title, e.g. "Delete rule 12?"
 * @property {string} description a line under it, or empty
 * @property {string} confirmLabel the button that says yes
 * @property {string[]} dependents what goes with it, shown as a list
 * @property {string} dependentsLabel heading over that list
 * @property {string} typed a name to type back before yes is enabled
 * @property {boolean} danger red button
 */

/**
 * One confirmation dialog for the whole app, mounted once in App.vue.
 * `ask` resolves true when the admin confirms and false when they close
 * the dialog any other way.
 */
export const useConfirmStore = defineStore('confirm', () => {
  /** @type {import('vue').Ref<ConfirmRequest | null>} */
  const request = ref(null)
  /** @type {((answer: boolean) => void) | null} */
  let resolve = null

  /**
   * @param {Partial<ConfirmRequest> & {question: string}} opts
   * @returns {Promise<boolean>}
   */
  function ask({
    question,
    description = '',
    confirmLabel = 'Delete',
    dependents = [],
    dependentsLabel = 'Also deleted',
    typed = '',
    danger = true,
  }) {
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
    return new Promise((r) => {
      resolve = r
    })
  }

  function settle(answer) {
    const r = resolve
    resolve = null
    request.value = null
    if (r) r(answer)
  }

  return { request, ask, settle }
})
