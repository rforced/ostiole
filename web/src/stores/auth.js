import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { ApiError, api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'

/** Beside a setting only an admin may change, for everyone else. */
export const ADMIN_ONLY = 'Only an admin can change this.'

/**
 * Tracks the current session and whether first-run setup is still needed.
 */
export const useAuthStore = defineStore('auth', () => {
  /** @type {import('vue').Ref<import('@/lib/api').Session | null>} */
  const user = ref(null)
  /** @type {import('vue').Ref<boolean | null>} null until known */
  const setupNeeded = ref(null)
  const ready = ref(false)

  const loggedIn = computed(() => user.value !== null)
  /**
   * Whether the account may change what only an admin may: command crons,
   * updates, the remote backup, management access and anti-lockout. The
   * server refuses those to anyone else at apply; the UI greys them out.
   */
  const isAdmin = computed(() => user.value?.role === 'admin')

  /** Resolve the session state once; safe to call repeatedly. */
  async function bootstrap() {
    if (ready.value) return
    try {
      user.value = await api.auth.me()
      setupNeeded.value = false
    } catch (e) {
      user.value = null
      if (e instanceof ApiError && e.status === 401) {
        try {
          setupNeeded.value = (await api.setup.status()).needed
        } catch {
          setupNeeded.value = null
        }
      } else {
        throw e
      }
    } finally {
      ready.value = true
    }
  }

  async function login(username, password) {
    user.value = await api.auth.login(username, password)
    setupNeeded.value = false
  }

  async function setup(username, password) {
    user.value = await api.setup.create(username, password)
    setupNeeded.value = false
  }

  async function logout() {
    try {
      await api.auth.logout()
    } finally {
      user.value = null
      useSystemStore().reset()
      useConfigStore().reset()
    }
  }

  /** Called when any request comes back 401. */
  function invalidate() {
    user.value = null
    useSystemStore().reset()
    useConfigStore().reset()
  }

  return {
    user,
    setupNeeded,
    ready,
    loggedIn,
    isAdmin,
    bootstrap,
    login,
    setup,
    logout,
    invalidate,
  }
})
