import { getCurrentScope, onScopeDispose, ref } from 'vue'

/**
 * Whether a media query matches, kept current as the window changes. Where
 * there is no matchMedia (jsdom) it never matches.
 *
 * @param {string} query
 * @returns {import('vue').Ref<boolean>}
 */
export function useMediaQuery(query) {
  const matches = ref(false)
  if (typeof window.matchMedia !== 'function') return matches
  const mql = window.matchMedia(query)
  matches.value = mql.matches
  const update = (e) => (matches.value = e.matches)
  mql.addEventListener('change', update)
  if (getCurrentScope()) onScopeDispose(() => mql.removeEventListener('change', update))
  return matches
}

/**
 * Narrower than Tailwind's lg, where the shell trades the sidebar for a top
 * bar and a drawer. Must match the max-lg: variant in the templates.
 */
export const useNarrow = () => useMediaQuery('(width < 64rem)')
