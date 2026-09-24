import { computed, ref } from 'vue'

import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

/** How often the sidecar is asked what it is doing while the page is shown. */
const POLL_MS = 5000

/** One read of the proxy status, shared by the page header and its notices. */
const status = ref(null)
let inflight = null

/** Fetches once however many callers ask while it is in the air. */
function fetchStatus() {
  if (!inflight) {
    inflight = api.proxy
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
 * What the proxy is doing, in the order a router goes through it: the
 * daemon, the ports, the apply, the draft. One caller polls and the rest
 * read what it left behind.
 *
 * @param {{poll?: boolean}} [opts] poll: own the read
 */
export function useProxyStatus({ poll = false } = {}) {
  const config = useConfigStore()
  if (poll) useAsync(fetchStatus, { interval: POLL_MS, immediate: true })

  const draft = computed(() => config.proxy)
  const saved = computed(() => config.saved?.services?.proxy ?? { enabled: false })

  const httpPort = computed(() => draft.value.httpPort || 80)
  const httpsPort = computed(() => draft.value.httpsPort || 443)

  /** A listener on the web UI's own port never comes up. */
  const clash = computed(() => {
    const web = config.draft?.system?.management?.webPort
    if (!draft.value.enabled || !web) return 0
    if (httpPort.value === web) return httpPort.value
    if (httpsPort.value === web) return httpsPort.value
    return (draft.value.routes ?? []).find((r) => r.enabled && r.port === web)?.port ?? 0
  })

  /** Whether a proxy configuration has anything to serve. */
  const serves = (p) =>
    Boolean(
      p?.enabled &&
      ((p.sites ?? []).some((s) => s.enabled) || (p.routes ?? []).some((r) => r.enabled)),
    )

  const stage = computed(() => {
    if (!status.value) return 'loading'
    if (!status.value.setUp) return 'missing'
    if (clash.value) return 'port-clash'
    if (!serves(saved.value)) return serves(draft.value) ? 'unapplied' : 'off'
    if (!status.value.running) return 'stopped'
    return 'running'
  })

  /**
   * The badge: what the router runs, from the applied configuration and
   * the daemon, never the draft. Not installed has no badge; the notice
   * under the header says so.
   */
  const state = computed(() => {
    if (!status.value?.setUp) return ''
    if (!serves(saved.value)) return 'off'
    return status.value.running ? 'running' : 'stopped'
  })

  return { status, stage, state, clash }
}
