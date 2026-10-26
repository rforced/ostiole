import { defineStore } from 'pinia'
import { ref } from 'vue'

import { api, type Status } from '@/lib/api'

/**
 * Engine status shared by the dashboard, the apply bar, and the router
 * guard that sends an unconfigured router to the setup wizard.
 */
export const useSystemStore = defineStore('system', () => {
  const status = ref<Status | null>(null)
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
