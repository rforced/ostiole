import { onBeforeUnmount, ref, watch, type Ref } from 'vue'

import { useAsync } from '@/lib/async'
import type { Config } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'

/** How long after the last edit the rows are read again. */
const DEBOUNCE_MS = 300

/**
 * Rows the router derives from the draft being edited rather than from
 * what is saved: the rules Ostiole adds around a zone's rules, the
 * automatic outbound NAT, the names the DNS service answers. They are read
 * on mount and again once edits pause, so a change shows before it is
 * applied. A read that fails, as it does while the draft does not
 * validate, keeps the rows of the last one that did.
 *
 * Rows that also follow the router, like the names devices hold, can poll:
 * interval sets the period, the poll runs from mount, and stop() and
 * start() switch it, as Live does.
 */
export function useDraftRows<T>(read: (draft: Config) => Promise<T[]>, { interval = 0 } = {}) {
  const config = useConfigStore()
  const rows = ref<T[]>([]) as Ref<T[]>
  const state = useAsync(
    async () => {
      if (!config.draft) return
      rows.value = await read(config.draft)
    },
    { immediate: true, interval },
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
