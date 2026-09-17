import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

/**
 * Tab selection kept in the URL hash, so a reload or a pasted link lands on
 * the tab you were looking at. The first value is the default and carries no
 * hash, which keeps the plain page URL clean; an unknown hash falls back to
 * it rather than showing nothing.
 *
 * Switching tabs replaces the history entry instead of pushing one, so Back
 * still means "the page before this one" and not "the tab before this one".
 *
 * @param {string[]} values tab values in the order the page lists them
 * @returns {import('vue').WritableComputedRef<string>} v-model for AppTabs
 */
export function useTabHash(values) {
  const route = useRoute()
  const router = useRouter()
  return computed({
    get() {
      const want = route.hash.slice(1)
      return values.includes(want) ? want : values[0]
    },
    set(value) {
      if (!values.includes(value)) return
      const hash = value === values[0] ? '' : `#${value}`
      if (hash !== route.hash) router.replace({ hash })
    },
  })
}

/**
 * The tabs the nav tree declares for the current page, and the open one as
 * a v-model for AppTabs. A page reads them from here so their labels and
 * order are written once, next to the page's sidebar entry.
 *
 * @returns {{tabs: import('./nav').Tab[], tab: import('vue').WritableComputedRef<string>}}
 */
export function usePageTabs() {
  const tabs = useRoute().meta.tabs ?? []
  return { tabs, tab: useTabHash(tabs.map((t) => t.value)) }
}
