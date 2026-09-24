<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { LEVELS } from '@/lib/meter'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import UpdateModeFields from '@/views/system/UpdateModeFields.vue'

/** How often a running update is looked at. */
const POLL_MS = 1500

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const current = ref('')
/** The version that served this page, which the script running it came from. */
const loaded = ref('')
const check = ref(null)
const status = ref(null)
const restartedTo = ref('')
/** When the latest above was last asked about: by the cron, or by the button. */
const lastCheck = ref('')
/** Why the last scheduled check failed, if it did. */
const checkError = ref('')

// The channel lives in the configuration rather than in this browser,
// because the scheduled update has to know which one to follow.
const settings = computed(() => config.ostioleUpdates)
const channel = computed(() => settings.value.channel || 'stable')

function setChannel(v) {
  config.setUpdates('ostiole', { channel: v })
  check.value = null
  lastCheck.value = ''
  checkError.value = ''
}

const running = computed(() =>
  ['checking', 'downloading', 'verifying', 'installing', 'restarting'].includes(
    status.value?.state,
  ),
)
const percent = computed(() =>
  status.value?.total ? Math.round((status.value.done / status.value.total) * 100) : null,
)

async function loadVersion() {
  try {
    current.value = (await api.health()).version
    if (!loaded.value) loaded.value = current.value
  } catch {
    /* shown elsewhere */
  }
}

// The button is the one thing here that asks GitHub; everything else
// draws itself from what the nightly check left behind.
const checking = useAsync(async () => {
  try {
    const res = await api.update.check(channel.value)
    check.value = res.check
    status.value = res.status
    lastCheck.value = new Date().toISOString()
    checkError.value = ''
  } catch (e) {
    throw e instanceof ApiError && e.status === 502
      ? new Error(`Could not reach GitHub: ${e.message}`)
      : e
  }
})

// Runs only while an update is in flight: started by install, or on mount
// when one was already running, and stopped by tick at the end.
const poll = useAsync(tick, { interval: POLL_MS })

async function tick() {
  // Which version answers is the only reliable sign the update landed.
  // The restart can happen between two polls, and the process that comes
  // back has never heard of it: it reports an idle updater and the same
  // cached check as before, which reads exactly like an update that was
  // never installed. Health is public and says who is answering.
  try {
    const h = await api.health()
    if (h.version) {
      current.value = h.version
      // A version this page was not served by is the restart, whether or
      // not the poll ever caught the state. The release the check named
      // is the one running now, so nothing is waiting.
      if (loaded.value && h.version !== loaded.value) {
        restartedTo.value = h.version
        if (check.value) check.value = { ...check.value, available: false, security: false }
      }
    }
  } catch {
    return // still restarting
  }
  try {
    status.value = (await api.update.status()).status
    // Idle is the new process answering: an update that is running never
    // goes back to idle in the process that started it.
    if (['failed', 'idle'].includes(status.value.state)) poll.stop()
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) poll.stop()
  }
}

const install = useAsync(async () => {
  status.value = await api.update.apply(channel.value)
  poll.start()
})

const busy = computed(() => checking.busy.value || install.busy.value)
const error = computed(() => checking.error.value || install.error.value)
const installing = computed(() => install.busy.value || running.value)

async function askInstall() {
  const ok = await confirm.ask({
    question: `Install ${check.value.latest}?`,
    description: 'The service restarts once it is in place.',
    confirmLabel: 'Install',
  })
  if (ok) await install.run()
}

function reload() {
  window.location.reload()
}

const when = (s) => (s ? new Date(s).toLocaleString() : 'never')

onMounted(async () => {
  await loadVersion()
  try {
    const res = await api.update.status()
    status.value = res.status
    // A router that has never checked, or whose last check failed, says
    // so rather than claiming to be up to date on an empty answer. A
    // cached answer about the other channel is nothing to go on either.
    if (res.check?.lastCheck && res.check.channel === channel.value) {
      lastCheck.value = res.check.lastCheck
      checkError.value = res.check.checkError || ''
      if (!res.check.checkError) check.value = res.check
    }
  } catch {
    /* updates unavailable */
  }
  if (!running.value) poll.stop()
})
</script>

<template>
  <SectionCard title="Ostiole updates">
    <template #actions>
      <RefreshButton
        :busy="checking.busy.value"
        :updated-at="checking.updatedAt.value"
        :disabled="running || !auth.isAdmin"
        label="Check now"
        busy-label="Checking…"
        @click="checking.run"
      />
      <button
        v-if="check?.available && !restartedTo"
        type="button"
        class="btn-primary"
        :disabled="busy || running || !auth.isAdmin"
        :aria-busy="installing"
        @click="askInstall"
      >
        <LoaderCircle v-if="installing" class="size-4 animate-spin" aria-hidden="true" />
        {{ installing ? 'Installing…' : `Install ${check.latest}` }}
      </button>
    </template>
    <div class="space-y-4">
      <dl class="kv max-w-md">
        <dt>Installed</dt>
        <dd class="font-mono">{{ current || '…' }}</dd>
        <dt>Latest</dt>
        <dd class="font-mono">{{ check ? check.latest || 'none' : 'not checked' }}</dd>
        <dt>Last checked</dt>
        <dd>{{ when(lastCheck) }}</dd>
      </dl>

      <p v-if="!auth.isAdmin" class="text-ink-muted">
        Only an admin can check, install or change how updates run.
      </p>

      <fieldset v-if="config.draft" class="space-y-4" :disabled="!auth.isAdmin">
        <UpdateModeFields
          prefix="ostiole-upd"
          :mode="settings.mode ?? ''"
          :check-schedule="settings.checkSchedule ?? ''"
          :install-schedule="settings.installSchedule ?? ''"
          security-note="A release only counts as a security release when its notes say so."
          @update:mode="config.setUpdates('ostiole', { mode: $event })"
          @update:check-schedule="config.setUpdates('ostiole', { checkSchedule: $event })"
          @update:install-schedule="config.setUpdates('ostiole', { installSchedule: $event })"
        />
        <p class="text-ink-muted">
          An install restarts the service and rolls back if the new version does not come up.
        </p>
      </fieldset>

      <div class="form-row">
        <FormField id="upd-channel" label="Channel">
          <select
            id="upd-channel"
            :value="channel"
            class="input w-40"
            :disabled="running || !auth.isAdmin"
            @change="setChannel($event.target.value)"
          >
            <option value="stable">Stable</option>
            <option value="beta">Beta (prereleases)</option>
          </select>
        </FormField>
      </div>

      <p v-if="check && !check.available && !running && !restartedTo" role="status" class="text-ok">
        Up to date.
      </p>

      <div v-if="check?.available && check.release" class="rounded-md border border-line p-3">
        <p class="font-medium">
          {{ check.release.tag
          }}<span v-if="check.security" class="badge badge-warn ml-1">security release</span
          ><span class="font-normal text-ink-muted"
            >&nbsp;· {{ new Date(check.release.publishedAt).toLocaleDateString() }}</span
          >
          <a :href="check.release.url" target="_blank" rel="noopener" class="link ml-2"
            >release page</a
          >
        </p>
        <pre
          v-if="check.release.notes"
          class="mt-2 max-h-48 overflow-auto font-sans whitespace-pre-wrap text-ink-2"
          >{{ check.release.notes }}</pre>
      </div>

      <div v-if="running" role="status" aria-live="polite">
        <p class="font-medium capitalize">
          {{ status.state }}<span v-if="status.version">&nbsp;{{ status.version }}</span
          ><span v-if="percent !== null" class="font-mono tabular-nums">&nbsp;{{ percent }}%</span>
        </p>
        <div
          v-if="percent !== null"
          class="meter mt-1 max-w-md"
          :class="LEVELS.ok.track"
          role="meter"
          aria-label="Update progress"
          :aria-valuenow="percent"
          aria-valuemin="0"
          aria-valuemax="100"
        >
          <div class="meter-fill" :class="LEVELS.ok.fill" :style="{ width: percent + '%' }"></div>
        </div>
        <p v-if="status.state === 'restarting'" class="mt-1 text-ink-muted">
          Waiting for the new version to answer…
        </p>
      </div>

      <p v-if="restartedTo" role="status" class="text-ok">
        Updated to <span class="font-mono">{{ restartedTo }}</span
        >. <button type="button" class="link" @click="reload">Reload the page</button> to load the
        new UI.
      </p>

      <AppNotice v-if="status?.state === 'failed'" kind="bad">
        Update failed: {{ status.message }}
      </AppNotice>
      <p v-if="checkError" role="alert" class="text-bad">The last check failed: {{ checkError }}</p>
      <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
    </div>
  </SectionCard>
</template>
