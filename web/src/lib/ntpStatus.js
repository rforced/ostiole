import { computed, ref } from 'vue'

import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'

/** How often the time service is asked what it is doing while its page is shown. */
const POLL_MS = 10000

/** One read, shared by the page header and the cards under it. */
const status = ref(null)
let inflight = null

/** Fetches once however many callers ask while it is in the air. */
function fetchStatus() {
  if (!inflight) {
    inflight = api.ntp
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

/** What each state the server reports a source in is called on the page. */
export const SOURCE_WORDS = {
  selected: 'in use',
  combined: 'agrees',
  excluded: 'standby',
  unusable: 'no answer',
  falseticker: 'wrong time',
  jittery: 'unsteady',
}

/** Best first: the source the clock follows, then the ones that count. */
const RANK = ['selected', 'combined', 'excluded', 'jittery', 'falseticker', 'unusable']

/**
 * The live sources that came from one configured server, best first: one
 * for a server, up to four for a pool. Names compare as DNS compares them.
 *
 * @param {string} host
 * @param {Array<{name: string, state: string}>} [sources]
 */
export function sourcesFor(host, sources) {
  const name = host.toLowerCase()
  return (sources ?? [])
    .filter((s) => s.name.toLowerCase() === name)
    .sort((a, b) => RANK.indexOf(a.state) - RANK.indexOf(b.state))
}

/**
 * What the time service is doing. The badge is about the router: none
 * before `ostiole repair` sets the service up or where the host keeps the
 * clock, otherwise running or stopped. One caller polls and the rest read
 * what it left behind.
 *
 * @param {{poll?: boolean}} [opts] poll: own the read
 */
export function useNtpStatus({ poll = false } = {}) {
  const read = poll ? useAsync(fetchStatus, { interval: POLL_MS, immediate: true }) : null
  const state = computed(() => {
    const s = status.value
    if (!s?.setUp || s.hostClock) return ''
    return s.running ? 'running' : 'stopped'
  })
  return { status, state, read }
}
