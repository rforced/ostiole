import { onBeforeUnmount, ref, watch } from 'vue'

import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

/** How long after the last edit the rows are read again. */
const DEBOUNCE_MS = 300

/**
 * Rows the router derives from the draft being edited rather than from
 * what is saved: the rules Ostiole adds around a zone's rules and the
 * names static leases answer. They are read on mount and again once edits
 * pause, so a change shows before it is applied. A read that fails, as it
 * does while the draft does not validate, keeps the rows of the last one
 * that did.
 *
 * @template T
 * @param {(draft: object) => Promise<T[]>} read
 */
export function useDraftRows(read) {
  const config = useConfigStore()
  /** @type {import('vue').Ref<T[]>} */
  const rows = ref([])
  const state = useAsync(
    async () => {
      if (!config.draft) return
      rows.value = await read(config.draft)
    },
    { immediate: true },
  )
  let timer = 0
  watch(
    () => config.draft,
    () => {
      window.clearTimeout(timer)
      timer = window.setTimeout(state.run, DEBOUNCE_MS)
    },
    { deep: true },
  )
  onBeforeUnmount(() => window.clearTimeout(timer))
  return { rows, ...state }
}
