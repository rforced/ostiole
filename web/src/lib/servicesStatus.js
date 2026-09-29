import { computed, ref } from 'vue'

import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

/** One read of the services status, shared by a page's header and its notices. */
const status = ref(null)
let inflight = null

/** Fetches once however many callers ask while it is in the air. */
function fetchStatus() {
  if (!inflight) {
    inflight = api.services
      .status()
      .then((s) => {
        status.value = s
      })
      .finally(() => {
        inflight = null
      })
  }
  return inflight
}

/**
 * What a service is doing, for the badge in its page header: off when the
 * applied configuration leaves it off, otherwise running or stopped with
 * the unit. One unit answers both DHCP and DNS, so each reports its own.
 *
 * @param {'dhcp' | 'dns' | 'upnp'} service
 * @param {{load?: boolean}} [opts] load: fetch on mount
 */
export function useServicesStatus(service, { load = false } = {}) {
  const config = useConfigStore()
  const read = useAsync(fetchStatus, { immediate: load })
  const state = computed(() => {
    if (!status.value || !config.loaded) return ''
    if (!config.saved?.services?.[service]?.enabled) return 'off'
    const running = service === 'upnp' ? status.value.upnpRunning : status.value.running
    return running ? 'running' : 'stopped'
  })
  return { status, state, read }
}
