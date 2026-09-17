<script setup>
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import UpdateModeFields from '@/views/system/UpdateModeFields.vue'

/** How many pending packages are listed before the rest are folded away. */
const SHOWN = 15
/** How often a running install is looked at. */
const POLL_MS = 3000

const config = useConfigStore()
const confirm = useConfirmStore()
const status = ref(null)
const actionError = ref('')
const showAll = ref(false)

const settings = computed(() => config.systemUpdates)
const mode = computed(() => settings.value.mode || status.value?.mode || 'security')
const packages = computed(() => status.value?.pending?.packages ?? [])
const shown = computed(() => (showAll.value ? packages.value : packages.value.slice(0, SHOWN)))
const securityCount = computed(() => status.value?.pending?.security ?? 0)
const running = computed(() => status.value?.running === true)

const excludes = computed({
  get: () => (settings.value.exclude ?? []).join(', '),
  set: (v) => config.setUpdates('system', { exclude: parseList(v) }),
})

/** What the install button does, which follows the mode unless it cannot. */
const installSecurityOnly = computed(
  () => mode.value === 'security' && status.value?.securityCapable === true,
)

// The first read is the first tick; the poll then keeps going only while
// an install runs.
const poll = useAsync(readStatus, { interval: POLL_MS, immediate: true })

async function readStatus() {
  try {
    status.value = await api.systemUpdates.status()
  } catch {
    // The endpoint is missing on a router with nothing to drive; the card
    // says so from what it already has.
    status.value = null
  }
  if (!running.value) poll.stop()
}

const check = useAsync(async () => {
  status.value = await api.systemUpdates.check()
})

const install = useAsync(async () => {
  status.value = await api.systemUpdates.apply(installSecurityOnly.value)
  poll.start()
})

const busy = computed(() => check.busy.value || install.busy.value)
const error = computed(() => actionError.value || check.error.value || install.error.value)

async function askInstall() {
  const n = installSecurityOnly.value ? securityCount.value : packages.value.length
  const ok = await confirm.ask({
    question: n ? `Install ${n} ${n === 1 ? 'update' : 'updates'}?` : 'Install updates?',
    description: 'The router may want a reboot afterwards.',
    confirmLabel: 'Install',
  })
  if (ok) await install.run()
}

async function reboot() {
  actionError.value = ''
  try {
    await api.systemUpdates.reboot()
    status.value = { ...status.value, rebootReason: 'rebooting now…' }
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

const when = (s) => (s ? new Date(s).toLocaleString() : 'never')
</script>

<template>
  <section class="card space-y-4" aria-labelledby="os-upd-title">
    <h2 id="os-upd-title" class="card-title">Operating system updates</h2>
    <p class="max-w-3xl text-sm text-neutral-500">
      The Linux underneath Ostiole, updated through its own package manager.
    </p>

    <dl class="kv max-w-md">
      <dt>System</dt>
      <dd>{{ status?.distro || 'unknown' }}</dd>
      <dt>Package manager</dt>
      <dd class="font-mono">{{ status?.manager || 'none found' }}</dd>
      <dt>Last checked</dt>
      <dd>{{ when(status?.lastCheck) }}</dd>
    </dl>

    <!-- These are part of the page rather than announcements, so they are
         plain text: a live region here would join the apply bar's. -->
    <p v-if="status && !status.available" class="text-sm text-amber-700 dark:text-amber-300">
      {{ status.unavailable }}
    </p>
    <p v-else-if="!status" class="text-sm text-neutral-500">
      Nothing on this router drives a package manager.
    </p>

    <template v-if="config.draft">
      <UpdateModeFields
        prefix="os-upd"
        :mode="settings.mode ?? ''"
        :check-schedule="settings.checkSchedule ?? ''"
        :install-schedule="settings.installSchedule ?? ''"
        :security-capable="status?.securityCapable !== false"
        :security-note="`${status?.manager ?? 'This package manager'} has no security-only channel.`"
        @update:mode="config.setUpdates('system', { mode: $event })"
        @update:check-schedule="config.setUpdates('system', { checkSchedule: $event })"
        @update:install-schedule="config.setUpdates('system', { installSchedule: $event })"
      />
      <FormField
        id="os-upd-exclude"
        label="Never upgrade"
        :hint="
          status && !status.excludeSupported
            ? `${status.manager} cannot hold packages back in this mode, so this list is not applied.`
            : 'Package names, comma separated, such as a kernel a driver is pinned to.'
        "
      >
        <input
          id="os-upd-exclude"
          v-model="excludes"
          class="input max-w-md font-mono"
          placeholder="kernel, kernel-core"
        />
      </FormField>
    </template>

    <div class="flex flex-wrap items-center gap-3">
      <RefreshButton
        :busy="check.busy.value"
        :updated-at="check.updatedAt.value"
        :disabled="running || !status?.available"
        label="Check now"
        busy-label="Checking…"
        @click="check.run"
      />
      <button
        type="button"
        class="btn-primary"
        :disabled="busy || running || !status?.available || !packages.length"
        @click="askInstall"
      >
        {{ installSecurityOnly ? 'Install security updates' : 'Install all updates' }}
      </button>
      <span v-if="running" role="status" class="text-sm text-neutral-500">
        Updating. This can take a while.
      </span>
    </div>

    <div v-if="status?.pending" class="text-sm">
      <p v-if="!packages.length" class="text-emerald-700 dark:text-emerald-300">
        Everything is up to date.
      </p>
      <p v-else>
        <span class="font-medium">{{ packages.length }} waiting</span>
        <span v-if="securityCount"> · {{ securityCount }} security</span>
      </p>
      <p v-if="status.pending.note" class="mt-1 text-sm text-neutral-500">
        {{ status.pending.note }}
      </p>
    </div>

    <div
      v-if="packages.length"
      class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800"
    >
      <table class="table">
        <thead>
          <tr>
            <th>Package</th>
            <th>Installed</th>
            <th>Available</th>
            <th>From</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-for="p in shown" :key="p.name">
            <td>
              <span class="font-mono">{{ p.name }}</span>
              <span v-if="p.security" class="badge badge-warn ml-2">security</span>
            </td>
            <td class="font-mono text-code">{{ p.from || '—' }}</td>
            <td class="font-mono text-code">{{ p.to || '—' }}</td>
            <td class="text-neutral-500">{{ p.repo || '—' }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
    <button v-if="packages.length > SHOWN" type="button" class="link" @click="showAll = !showAll">
      {{ showAll ? 'Show fewer' : `Show all ${packages.length}` }}
    </button>

    <div
      v-if="status?.rebootRequired"
      role="alert"
      class="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-900 dark:bg-amber-950/40"
    >
      <p class="font-medium text-amber-800 dark:text-amber-300">This router wants a reboot</p>
      <p class="text-amber-800/80 dark:text-amber-300/80">{{ status.rebootReason }}</p>
      <ConfirmButton
        class="mt-1"
        label="Reboot now"
        question="Reboot this firewall?"
        description="Everything behind it loses its connection until it is back."
        confirm-label="Reboot"
        typed="reboot"
        @confirm="reboot"
      />
    </div>

    <div v-if="status?.lastRun" class="text-sm">
      <p class="text-neutral-500">
        Last run {{ when(status.lastRun)
        }}<span v-if="status.lastMode"> ({{ status.lastMode }})</span>
      </p>
      <p v-if="status.lastError" role="alert" class="text-red-600 dark:text-red-400">
        {{ status.lastError }}
      </p>
      <pre
        v-if="status.lastOutput"
        class="mt-1 max-h-48 overflow-auto rounded bg-neutral-50 p-2 font-mono text-code whitespace-pre-wrap text-neutral-700 dark:bg-neutral-900 dark:text-neutral-300"
        >{{ status.lastOutput }}</pre>
    </div>

    <p v-if="status?.checkError" role="alert" class="text-sm text-red-600 dark:text-red-400">
      The last check failed: {{ status.checkError }}
    </p>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
  </section>
</template>
