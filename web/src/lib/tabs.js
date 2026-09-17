import { computed, toValue } from 'vue'
import { useRoute, useRouter } from 'vue-router'

/**
 * A one-of-many choice kept in the URL hash, so a reload or a pasted link
 * lands on what you were looking at: the tabs of a page, and the zone the
 * rules page is showing. The first value is the default and carries no hash,
 * which keeps the plain page URL clean; an unknown hash falls back to it
 * rather than showing nothing.
 *
 * Switching replaces the history entry instead of pushing one, so Back still
 * means "the page before this one" and not "the tab before this one".
 *
 * @param {import('vue').MaybeRefOrGetter<string[]>} values the values in the
 *   order the page lists them, as a getter when they are configuration that
 *   arrives after the page does; empty means nothing to choose yet.
 * @returns {import('vue').WritableComputedRef<string>} v-model for AppTabs
 */
export function useTabHash(values) {
  const route = useRoute()
  const router = useRouter()
  return computed({
    get() {
      const list = toValue(values)
      const want = route.hash.slice(1)
      return (list.includes(want) ? want : list[0]) ?? ''
    },
    set(value) {
      const list = toValue(values)
      if (!list.includes(value)) return
      const hash = value === list[0] ? '' : `#${value}`
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
