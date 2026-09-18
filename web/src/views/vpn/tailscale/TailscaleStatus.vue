<script setup>
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

/** How often the node is asked what it is doing while the tab is shown. */
const POLL_MS = 5000

const config = useConfigStore()
const status = ref(null)
const authUrl = ref('')
const authKey = ref('')

const poll = useAsync(
  async () => {
    status.value = await api.tailscale.status()
    // The daemon's own link replaces ours as soon as it has one, and goes
    // away when the login is through.
    if (status.value.authUrl) authUrl.value = status.value.authUrl
    else if (status.value.state === 'Running') authUrl.value = ''
  },
  { interval: POLL_MS, immediate: true },
)

const inDraft = computed(() => Boolean(config.tailscale))
const inSaved = computed(() => (config.saved?.interfaces ?? []).some((i) => i.tailscale))

/**
 * What the card says, in the order a router goes through: the daemon,
 * the draft, the apply, the login.
 */
const stage = computed(() => {
  if (!status.value) return 'loading'
  if (!status.value.setUp) return 'missing'
  if (!inDraft.value) return 'unjoined'
  if (!inSaved.value) return 'unapplied'
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

/** The badge beside the card title, per daemon state. */
const BADGE = {
  running: { text: 'logged in', tone: 'badge-ok' },
  'needs-login': { text: 'not logged in', tone: 'badge-warn' },
  'needs-approval': { text: 'waiting for approval', tone: 'badge-warn' },
  starting: { text: 'starting', tone: '' },
  stopped: { text: 'stopped', tone: 'badge-warn' },
}
const badge = computed(() => BADGE[stage.value])

/** The name as people write it, without the dot the daemon appends. */
const name = computed(() =>
  (status.value?.dnsName || status.value?.hostName || '').replace(/\.$/, ''),
)

/** Within a fortnight of expiry is worth saying; before that it is noise. */
const expiring = computed(() => {
  const at = status.value?.keyExpiry
  if (!at) return ''
  const days = (new Date(at).getTime() - Date.now()) / 86400000
  return days > 14 ? '' : new Date(at).toLocaleDateString()
})

const login = useAsync(async (key) => {
  const out = await api.tailscale.login(key)
  authUrl.value = out.authUrl ?? ''
  authKey.value = ''
  await poll.run()
})

const logout = useAsync(async () => {
  status.value = await api.tailscale.logout()
  authUrl.value = ''
})
</script>

<template>
  <div v-if="status">
    <p
      v-if="stage === 'missing'"
      class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
      role="note"
    >
      Not on this router. Run <code class="font-mono">ostiole repair --tailscale</code> as root
      once.
    </p>
    <p v-else-if="stage === 'unjoined'" class="text-sm text-neutral-500">Not joined.</p>
    <p v-else-if="stage === 'unapplied'" class="text-sm text-neutral-500">
      Apply the draft to start it.
    </p>

    <section v-else class="card space-y-3" aria-label="Node">
      <div class="flex flex-wrap items-baseline justify-between gap-3">
        <h2 class="section-title">
          Node
          <span class="badge ml-2" :class="badge.tone">{{ badge.text }}</span>
        </h2>
        <ConfirmButton
          v-if="stage === 'running'"
          label="Log out"
          question="Log this router out of its tailnet?"
          description="The node stays in the admin console, so logging in again puts it back."
          @confirm="logout.run()"
        />
      </div>

      <template v-if="stage === 'running'">
        <dl class="kv">
          <dt>Name</dt>
          <dd class="font-mono">{{ name }}</dd>
          <template v-if="status.ips.length">
            <dt>Addresses</dt>
            <dd class="font-mono">{{ status.ips.join(', ') }}</dd>
          </template>
          <template v-if="status.tailnet">
            <dt>Tailnet</dt>
            <dd>{{ status.tailnet }}</dd>
          </template>
          <template v-if="expiring">
            <dt>Key</dt>
            <dd>expires {{ expiring }}</dd>
          </template>
        </dl>
        <p v-for="h in status.health" :key="h" class="text-amber-700 dark:text-amber-300">
          {{ h }}
        </p>
      </template>

      <p v-else-if="stage === 'needs-approval'" class="text-neutral-500">
        Approve this router in the admin console.
      </p>
      <p v-else-if="stage === 'starting'" class="text-neutral-500">Starting.</p>
      <p v-else-if="stage === 'stopped'" class="text-neutral-500">Stopped.</p>

      <template v-else-if="stage === 'needs-login'">
        <form class="flex flex-wrap items-center gap-3" @submit.prevent="login.run(authKey)">
          <button
            type="button"
            class="btn-primary"
            :disabled="login.busy.value"
            @click="login.run('')"
          >
            Log in
          </button>
          <label for="ts-auth-key" class="text-neutral-500">or with an auth key</label>
          <input
            id="ts-auth-key"
            v-model="authKey"
            type="password"
            class="input w-72 font-mono"
            autocomplete="off"
            spellcheck="false"
          />
          <button type="submit" class="btn-secondary" :disabled="!authKey || login.busy.value">
            Log in with key
          </button>
        </form>
        <p class="text-neutral-500">A key is used once and not kept.</p>
        <p v-if="authUrl">
          Open
          <a :href="authUrl" target="_blank" rel="noreferrer" class="link">{{ authUrl }}</a>
          <span class="ml-1 text-neutral-500">Waiting for the login.</span>
        </p>
        <p v-if="login.error.value" role="alert" class="text-red-600 dark:text-red-400">
          {{ login.error.value }}
        </p>
      </template>
    </section>
  </div>
</template>
