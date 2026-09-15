<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'

const CHANNEL_KEY = 'ostiole.updateChannel'

const channel = ref(localStorage.getItem(CHANNEL_KEY) === 'beta' ? 'beta' : 'stable')
const current = ref('')
const check = ref(null)
const status = ref(null)
const error = ref('')
const busy = ref(false)
const restartedTo = ref('')

let poll = 0

function setChannel(v) {
  channel.value = v
  localStorage.setItem(CHANNEL_KEY, v)
  check.value = null
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

async function runCheck() {
  error.value = ''
  busy.value = true
  try {
    const res = await api.update.check(channel.value)
    check.value = res.check
    status.value = res.status
  } catch (e) {
    error.value =
      e instanceof ApiError && e.status === 502
        ? `Could not reach GitHub: ${e.message}`
        : String(e.message ?? e)
  } finally {
    busy.value = false
  }
}

async function install() {
  error.value = ''
  busy.value = true
  try {
    status.value = await api.update.apply(channel.value)
    startPolling()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

function startPolling() {
  stopPolling()
  poll = window.setInterval(tick, 1500)
}

function stopPolling() {
  if (poll) window.clearInterval(poll)
  poll = 0
}

async function tick() {
  if (status.value?.state === 'restarting') {
    // The daemon is coming back as the new version; health is public.
    try {
      const h = await api.health()
      if (h.version !== current.value) {
        restartedTo.value = h.version
        stopPolling()
      }
    } catch {
      /* still restarting */
    }
    return
  }
  try {
    status.value = await api.update.status()
    if (status.value.state === 'failed') stopPolling()
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) stopPolling()
  }
}

function reload() {
  window.location.reload()
}

onMounted(async () => {
  await loadVersion()
  try {
    status.value = await api.update.status()
    if (running.value) startPolling()
  } catch {
    /* updates unavailable */
  }
})
onBeforeUnmount(stopPolling)
</script>

<template>
  <section class="card space-y-4" aria-labelledby="upd-title">
    <h2 id="upd-title" class="card-title">Updates</h2>
    <dl class="kv max-w-md">
      <dt>Installed</dt>
      <dd class="font-mono">{{ current || '…' }}</dd>
      <dt>Latest</dt>
      <dd class="font-mono">{{ check ? check.latest || 'none' : 'not checked' }}</dd>
    </dl>

    <p v-if="status?.packageManaged" class="text-sm text-neutral-500">
      This binary came from a distro package. Update it with your package manager.
    </p>

    <div class="flex flex-wrap items-end gap-3">
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
      <button type="button" class="btn-secondary" :disabled="busy || running" @click="runCheck">
        Check for updates
      </button>
      <button
        v-if="check?.available && !status?.packageManaged"
        type="button"
        class="btn-primary"
        :disabled="busy || running"
        @click="install"
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
      <p class="mt-2 text-neutral-500">
        The download is verified against checksums signed with the key built into this binary. The
        service restarts afterwards and rolls back automatically if the new version fails its health
        check.
      </p>
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
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
  </section>
</template>
