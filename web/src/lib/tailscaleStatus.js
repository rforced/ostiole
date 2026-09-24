import { computed, ref } from 'vue'

import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

/** How often the node is asked what it is doing while the page is shown. */
const POLL_MS = 5000

/** One read of the node's state, shared by the page header and the card. */
const status = ref(null)
/** The daemon's login link, kept while the login is in the air. */
const authUrl = ref('')

/**
 * The word for the badge, for each stage the node can be in. The stages
 * follow the applied configuration, so the badge says what the router is
 * doing: a node only in the draft is still off.
 */
const STATE = {
  running: 'connected',
  'needs-login': 'disconnected',
  'needs-approval': 'waiting for approval',
  starting: 'starting',
  stopped: 'stopped',
  unjoined: 'off',
  unapplied: 'off',
  // Not installed: the notice under the header says so, and a badge that
  // said 'stopped' would be a different claim.
  missing: '',
  loading: '',
}

/**
 * What Tailscale is doing, in the order a router goes through it: the
 * daemon, the apply, the login. One caller polls and the rest read what it
 * left behind.
 *
 * @param {{poll?: boolean}} [opts] poll: own the read
 */
export function useTailscaleStatus({ poll = false } = {}) {
  const config = useConfigStore()

  const read = useAsync(
    async () => {
      status.value = await api.tailscale.status()
      // The daemon's own link replaces ours as soon as it has one, and goes
      // away when the login is through.
      if (status.value.authUrl) authUrl.value = status.value.authUrl
      else if (status.value.state === 'Running') authUrl.value = ''
    },
    poll ? { interval: POLL_MS, immediate: true } : {},
  )

  const inDraft = computed(() => Boolean(config.tailscale))
  const inSaved = computed(() => (config.saved?.interfaces ?? []).some((i) => i.tailscale))

  const stage = computed(() => {
    if (!status.value) return 'loading'
    if (!status.value.setUp) return 'missing'
    if (!inSaved.value) return inDraft.value ? 'unapplied' : 'unjoined'
    if (!status.value.running || status.value.state === 'Stopped') return 'stopped'
    switch (status.value.state) {
      case 'Running':
        return 'running'
      case 'NeedsLogin':
        return 'needs-login'
      case 'NeedsMachineAuth':
        return 'needs-approval'
      default:
        return 'starting'
    }
  })

  const state = computed(() => STATE[stage.value] ?? '')

  return { status, authUrl, stage, state, read }
}
