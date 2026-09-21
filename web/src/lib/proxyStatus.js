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

/** The word for the badge, for each stage the proxy can be in. */
const STATE = {
  running: 'running',
  stopped: 'stopped',
  off: 'off',
  unapplied: 'not applied',
  // Not installed: the notice under the header says so, and a badge that
  // said 'stopped' would be a different claim.
  missing: '',
  'port-clash': 'stopped',
  loading: '',
}

/**
 * What the proxy is doing, in the order a router goes through it: the
 * daemon, the ports, the draft, the apply. One caller polls and the rest
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

  /** Whether the draft has anything for the proxy to serve. */
  const serves = (p) =>
    Boolean(
      p?.enabled &&
      ((p.sites ?? []).some((s) => s.enabled) || (p.routes ?? []).some((r) => r.enabled)),
    )

  const stage = computed(() => {
    if (!status.value) return 'loading'
    if (!status.value.setUp) return 'missing'
    if (clash.value) return 'port-clash'
    if (!draft.value.enabled) return 'off'
    if (!serves(saved.value)) return 'unapplied'
    if (!status.value.running) return 'stopped'
    return 'running'
  })

  const state = computed(() => STATE[stage.value] ?? '')

  return { status, stage, state, clash }
}
