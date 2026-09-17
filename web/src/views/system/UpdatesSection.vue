<script setup>
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import UpdateModeFields from '@/views/system/UpdateModeFields.vue'

/** How often a running update is looked at. */
const POLL_MS = 1500

const config = useConfigStore()
const confirm = useConfirmStore()
const current = ref('')
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
  if (status.value?.state === 'restarting') {
    // The daemon is coming back as the new version; health is public.
    try {
      const h = await api.health()
      if (h.version !== current.value) {
        restartedTo.value = h.version
        poll.stop()
      }
    } catch {
      /* still restarting */
    }
    return
  }
  try {
    status.value = (await api.update.status()).status
    if (status.value.state === 'failed') poll.stop()
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
  <section class="card space-y-4" aria-labelledby="upd-title">
    <h2 id="upd-title" class="card-title">Ostiole updates</h2>
    <dl class="kv max-w-md">
      <dt>Installed</dt>
      <dd class="font-mono">{{ current || '…' }}</dd>
      <dt>Latest</dt>
      <dd class="font-mono">{{ check ? check.latest || 'none' : 'not checked' }}</dd>
      <dt>Last checked</dt>
      <dd>{{ when(lastCheck) }}</dd>
    </dl>

    <p v-if="status?.packageManaged" class="text-sm text-neutral-500">
      This binary came from a distro package, so the operating system updates below are what upgrade
      it.
    </p>

    <template v-if="config.draft">
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
      <p class="text-sm text-neutral-500">
        An install restarts the service and rolls back if the new version does not come up.
      </p>
    </template>

    <div class="form-row">
      <FormField id="upd-channel" label="Channel">
        <select
          id="upd-channel"
          :value="channel"
          class="input w-40"
          :disabled="running"
          @change="setChannel($event.target.value)"
        >
          <option value="stable">Stable</option>
          <option value="beta">Beta (prereleases)</option>
        </select>
      </FormField>
      <RefreshButton
        :busy="checking.busy.value"
        :updated-at="checking.updatedAt.value"
        :disabled="running"
        label="Check for updates"
        busy-label="Checking…"
        @click="checking.run"
      />
      <button
        v-if="check?.available && !status?.packageManaged"
        type="button"
        class="btn-primary"
        :disabled="busy || running"
        @click="askInstall"
      >
        Install {{ check.latest }}
      </button>
    </div>

    <p
      v-if="check && !check.available && !running && !restartedTo"
      role="status"
      class="text-sm text-emerald-700 dark:text-emerald-300"
    >
      Up to date.
    </p>

    <div
      v-if="check?.available && check.release"
      class="rounded-md border border-neutral-200 p-3 text-sm dark:border-neutral-800"
    >
      <p class="font-medium">
        {{ check.release.tag }}
        <span v-if="check.security" class="badge badge-warn ml-1">security release</span>
        <span class="font-normal text-neutral-500"
          >· {{ new Date(check.release.publishedAt).toLocaleDateString() }}</span
        >
        <a :href="check.release.url" target="_blank" rel="noopener" class="link ml-2"
          >release page</a
        >
      </p>
      <pre
        v-if="check.release.notes"
        class="mt-2 max-h-48 overflow-auto font-sans whitespace-pre-wrap text-neutral-700 dark:text-neutral-300"
        >{{ check.release.notes }}</pre>
    </div>

    <div v-if="running" role="status" aria-live="polite" class="text-sm">
      <p class="font-medium capitalize">
        {{ status.state }}<span v-if="status.version"> {{ status.version }}</span>
        <span v-if="percent !== null" class="font-mono tabular-nums"> {{ percent }}%</span>
      </p>
      <div
        v-if="percent !== null"
        class="mt-1 h-1.5 w-full max-w-md overflow-hidden rounded bg-neutral-200 dark:bg-neutral-800"
      >
        <div class="h-full bg-sky-600 transition-[width]" :style="{ width: percent + '%' }"></div>
      </div>
      <p v-if="status.state === 'restarting'" class="mt-1 text-neutral-500">
        Waiting for the new version to answer…
      </p>
    </div>

    <p v-if="restartedTo" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">
      Updated to <span class="font-mono">{{ restartedTo }}</span
      >. <button type="button" class="link" @click="reload">Reload the page</button> to load the new
      UI.
    </p>

    <p
      v-if="status?.state === 'failed'"
      role="alert"
      class="text-sm text-red-600 dark:text-red-400"
    >
      Update failed: {{ status.message }}
    </p>
    <p v-if="checkError" role="alert" class="text-sm text-red-600 dark:text-red-400">
      The last check failed: {{ checkError }}
    </p>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
  </section>
</template>
