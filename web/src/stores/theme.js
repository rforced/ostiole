import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

/** @typedef {'light' | 'dark' | 'system'} ThemePreference */
/** @typedef {'light' | 'dark'} ResolvedTheme */

/** Must match public/theme-init.js. */
export const THEME_STORAGE_KEY = 'ostiole.theme'

/**
 * The browser's own bar takes the top bar's colour, --surface in each
 * theme. Must match public/theme-init.js.
 */
export const THEME_COLORS = { light: '#ffffff', dark: '#171717' }

const DARK_QUERY = '(prefers-color-scheme: dark)'
const PREFERENCES = ['light', 'dark', 'system']

/** @returns {value is ThemePreference} */
export function isThemePreference(value) {
  return PREFERENCES.includes(value)
}

/** @returns {ThemePreference} */
function readStoredPreference() {
  try {
    const v = localStorage.getItem(THEME_STORAGE_KEY)
    if (isThemePreference(v)) return v
  } catch {
    /* storage unavailable */
  }
  return 'system'
}

function systemPrefersDark() {
  return typeof window.matchMedia === 'function' && window.matchMedia(DARK_QUERY).matches
}

export const useThemeStore = defineStore('theme', () => {
  const preference = ref(readStoredPreference())
  const systemDark = ref(systemPrefersDark())

  /** @type {import('vue').ComputedRef<ResolvedTheme>} */
  const resolved = computed(() => {
    if (preference.value === 'system') return systemDark.value ? 'dark' : 'light'
    return preference.value
  })

  function apply() {
    const root = document.documentElement
    root.classList.toggle('dark', resolved.value === 'dark')
    root.style.colorScheme = resolved.value
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute('content', THEME_COLORS[resolved.value])
  }

  /** Attach the OS listener and apply the current theme. Call once at startup. */
  function init() {
    if (typeof window.matchMedia === 'function') {
      const mql = window.matchMedia(DARK_QUERY)
      systemDark.value = mql.matches
      mql.addEventListener('change', (e) => {
        systemDark.value = e.matches
      })
    }
    apply()
  }

  /** @param {ThemePreference} next */
  function set(next) {
    preference.value = next
    try {
      if (next === 'system') localStorage.removeItem(THEME_STORAGE_KEY)
      else localStorage.setItem(THEME_STORAGE_KEY, next)
    } catch {
      /* storage unavailable */
    }
  }

  watch(resolved, apply)

  return { preference, resolved, init, set }
})
