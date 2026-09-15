import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { ApiError, api } from '@/lib/api'

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
    }
  }

  /** Called when any request comes back 401. */
  function invalidate() {
    user.value = null
  }

  return { user, setupNeeded, ready, loggedIn, bootstrap, login, setup, logout, invalidate }
})
