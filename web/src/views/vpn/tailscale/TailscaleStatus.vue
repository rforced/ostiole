<script setup>
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
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
 * What the strip says, in the order a router goes through: the daemon,
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
  <div v-if="status" class="space-y-3">
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
    <p v-else-if="stage === 'stopped'" class="text-sm text-neutral-500">Stopped.</p>
    <p v-else-if="stage === 'starting'" class="text-sm text-neutral-500">Starting.</p>
    <p v-else-if="stage === 'needs-approval'" class="text-sm text-neutral-500">
      Waiting for approval in the admin console.
    </p>

    <template v-else-if="stage === 'needs-login'">
      <p class="text-sm text-neutral-500">Not logged in.</p>
      <div class="flex flex-wrap items-end gap-3">
        <button
          type="button"
          class="btn-secondary"
          :disabled="login.busy.value"
          @click="login.run('')"
        >
          Log in
        </button>
        <FormField id="ts-auth-key" label="Auth key" hint="Used once and not kept.">
          <input id="ts-auth-key" v-model="authKey" type="password" class="input" />
        </FormField>
        <button
          type="button"
          class="btn-secondary"
          :disabled="!authKey || login.busy.value"
          @click="login.run(authKey)"
        >
          Log in with key
        </button>
      </div>
      <p v-if="authUrl" class="text-sm">
        <a :href="authUrl" target="_blank" rel="noreferrer" class="underline">{{ authUrl }}</a>
        <span class="ml-2 text-neutral-500">Waiting for the login.</span>
      </p>
      <p v-if="login.error.value" role="alert" class="text-sm text-red-600 dark:text-red-400">
        {{ login.error.value }}
      </p>
    </template>

    <template v-else-if="stage === 'running'">
      <p class="text-sm text-neutral-500">
        <span class="font-mono">{{ status.dnsName }}</span>
        <template v-if="status.ips.length">
          · <span class="font-mono">{{ status.ips.join(', ') }}</span>
        </template>
        <template v-if="status.tailnet"> · tailnet {{ status.tailnet }}</template>
      </p>
      <p v-if="expiring" class="text-sm text-neutral-500">Key expires {{ expiring }}.</p>
      <p v-for="h in status.health" :key="h" class="text-sm text-amber-700 dark:text-amber-300">
        {{ h }}
      </p>
      <ConfirmButton
        label="Log out"
        question="Log this router out of its tailnet?"
        description="The node stays in the admin console, so logging in again puts it back."
        @confirm="logout.run()"
      />
    </template>
  </div>
</template>
