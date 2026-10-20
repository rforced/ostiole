import { ApiError, api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { newId } from '@/lib/ids'

/** How long an apply whose reply was lost waits for the router to answer. */
export const LOST_MS = 20_000
/** How long between those reads. */
const RETRY_MS = 1000

/**
 * Applies a configuration with a confirmation window and resolves the
 * reply. A browser drops every request in flight when the computer's
 * addresses change, and an apply can change them, so a lost reply is not
 * a failed apply: the status names the apply awaiting confirmation by the
 * tag it was sent with. That one resolves as its reply would have.
 *
 * @param {object} config
 * @param {string} baseRevision
 * @param {number} seconds the confirmation window
 * @returns {Promise<{ pending: boolean, deadline: string }>}
 */
export async function applyWithConfirm(config, baseRevision, seconds) {
  const tag = newId('apply')
  try {
    return await api.config.apply(config, baseRevision, seconds, tag)
  } catch (e) {
    if (e instanceof ApiError) throw e
    const why = errorMessage(e).replace(/\.$/, '')
    const status = await answer()
    if (status?.pending?.tag === tag) return { pending: true, deadline: status.pending.deadline }
    if (status) throw new Error(`Nothing was applied: ${why}.`, { cause: e })
    throw new Error(
      `The router did not answer: ${why}. If it took the apply, the previous configuration comes back within ${seconds}s.`,
      { cause: e },
    )
  }
}

/**
 * Reads the status until the router answers, or null once LOST_MS has
 * passed without a word. A refusal is an answer, and is thrown.
 */
async function answer() {
  const until = Date.now() + LOST_MS
  for (;;) {
    try {
      return await api.status()
    } catch (e) {
      if (e instanceof ApiError) throw e
      if (Date.now() >= until) return null
    }
    await new Promise((resolve) => window.setTimeout(resolve, RETRY_MS))
  }
}
