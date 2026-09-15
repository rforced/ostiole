import { defineStore } from 'pinia'
import { ref } from 'vue'

import { api } from '@/lib/api'

/**
 * Engine status shared by the dashboard, the apply bar, and the router
 * guard that sends an unconfigured box to the setup wizard.
 */
export const useSystemStore = defineStore('system', () => {
  /** @type {import('vue').Ref<import('@/lib/api').Status | null>} */
  const status = ref(null)
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

  function reset() {
    status.value = null
  }

  return { status, error, refresh, reset }
})
