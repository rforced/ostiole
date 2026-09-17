import { defineStore } from 'pinia'
import { ref } from 'vue'

import { api } from '@/lib/api'

/**
 * Engine status shared by the dashboard, the apply bar, and the router
 * guard that sends an unconfigured router to the setup wizard — and, one
 * layer down, what the router still needs before it can be a firewall at
 * all.
 */
export const useSystemStore = defineStore('system', () => {
  /** @type {import('vue').Ref<import('@/lib/api').Status | null>} */
  const status = ref(null)
  /**
   * The host report, or null before it has been read. The guard reads
   * `prepared`; the host page reads the rest.
   *
   * @type {import('vue').Ref<object | null>}
   */
  const host = ref(null)
  const error = ref('')

  async function refresh() {
    try {
      status.value = await api.status()
      error.value = ''
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    }
    return status.value
  }

  /**
   * Read what the router still needs. One that cannot answer is taken
   * as prepared: the gate must never be what stops somebody reaching the
   * UI to fix whatever is wrong with it.
   */
  async function refreshHost() {
    try {
      host.value = await api.host.status()
    } catch {
      host.value = { prepared: true }
    }
    return host.value
  }

  function reset() {
    status.value = null
    host.value = null
  }

  return { status, host, error, refresh, refreshHost, reset }
})
