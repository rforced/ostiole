<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { useTailscaleStatus } from '@/lib/tailscaleStatus'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

/** The notices and the facts under Tailscale's page header. It owns the read. */
const { status, authUrl, stage, read } = useTailscaleStatus({ poll: true })
const authKey = ref('')

/** The name as people write it, without the dot the daemon appends. */
const name = computed(() =>
  (status.value?.dnsName || status.value?.hostName || '').replace(/\.$/, ''),
)

/** Within a fortnight of expiry is worth saying; before that it is noise. */
const expiring = computed(() => {
  const at = status.value?.keyExpiry
  if (!at) return ''
  const days = (new Date(at).getTime() - Date.now()) / 86400000
  return days > 14 ? '' : formatWhen(at)
})

const login = useAsync(async (key) => {
  const out = await api.tailscale.login(key)
  authUrl.value = out.authUrl ?? ''
  authKey.value = ''
  await read.run()
})
/** Which way the login in flight went: 'link', 'key' or ''. */
const via = ref('')

async function logIn(key) {
  via.value = key ? 'key' : 'link'
  await login.run(key)
  via.value = ''
}

const logout = useAsync(async () => {
  status.value = await api.tailscale.logout()
  authUrl.value = ''
})
</script>

<template>
  <div v-if="status">
    <AppNotice v-if="stage === 'missing'">
      Not on this router. Run <code class="font-mono">ostiole repair --tailscale</code> as root
      once.
    </AppNotice>
    <p v-else-if="stage === 'unjoined'" class="text-sm text-ink-muted">Not joined.</p>
    <p v-else-if="stage === 'unapplied'" class="text-sm text-ink-muted">
      Apply the draft to start it.
    </p>

    <SectionCard v-else title="Node">
      <template v-if="stage === 'running'" #actions>
        <ConfirmButton
          label="Log out"
          :disabled="!auth.isAdmin"
          :title="auth.isAdmin ? undefined : 'Only an admin can log the router out.'"
          question="Log this router out of its tailnet?"
          description="The node stays in the admin console, so logging in again puts it back."
          @confirm="logout.run()"
        />
      </template>

      <div class="space-y-3">
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
          <AppNotice v-for="h in status.health" :key="h">{{ h }}</AppNotice>
        </template>

        <p v-else-if="stage === 'needs-approval'" class="text-ink-muted">
          Approve this router in the admin console.
        </p>
        <p v-else-if="stage === 'starting'" class="text-ink-muted">Starting.</p>
        <p v-else-if="stage === 'stopped'" class="text-ink-muted">Stopped.</p>

        <p v-else-if="stage === 'needs-login' && auth.readOnly" class="text-ink-muted">
          Not logged in.
        </p>
        <template v-else-if="stage === 'needs-login'">
          <form class="flex flex-wrap items-center gap-3" @submit.prevent="logIn(authKey)">
            <ActionButton
              kind="primary"
              label="Log in"
              busy-label="Logging in…"
              :busy="via === 'link'"
              :disabled="login.busy.value"
              @click="logIn('')"
            />
            <label for="ts-auth-key" class="text-ink-muted">or with an auth key</label>
            <input
              id="ts-auth-key"
              v-model="authKey"
              type="password"
              class="input w-48 font-mono max-sm:w-full"
              autocomplete="off"
              spellcheck="false"
            />
            <ActionButton
              type="submit"
              label="Log in with key"
              busy-label="Logging in…"
              :busy="via === 'key'"
              :disabled="!authKey || login.busy.value"
            />
          </form>
          <p class="text-ink-muted">A key is used once and not kept.</p>
          <p v-if="authUrl">
            Open
            <a :href="authUrl" target="_blank" rel="noreferrer" class="link">{{ authUrl }}</a>
            <span class="ml-1 text-ink-muted">
              <LoaderCircle class="mr-1 inline size-4 animate-spin" aria-hidden="true" />Waiting for
              the login.
            </span>
          </p>
          <ErrorLine v-if="login.error.value">{{ login.error.value }}</ErrorLine>
        </template>
      </div>
    </SectionCard>
  </div>
</template>
