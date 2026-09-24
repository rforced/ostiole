<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { parseList } from '@/lib/lists'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import UpdateModeFields from '@/views/system/UpdateModeFields.vue'

/** How many pending packages are listed before the rest are folded away. */
const SHOWN = 15
/** How often a running install is looked at. */
const POLL_MS = 3000

const auth = useAuthStore()
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
const installing = computed(() => install.busy.value || running.value)
const installLabel = computed(() => {
  if (installing.value) return 'Installing…'
  return installSecurityOnly.value ? 'Install security updates' : 'Install all updates'
})

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
  <div class="space-y-5">
    <SectionCard
      title="Operating system updates"
      intro="The Linux underneath Ostiole, updated through its own package manager."
    >
      <template #actions>
        <RefreshButton
          v-if="!auth.readOnly"
          :busy="check.busy.value"
          :updated-at="check.updatedAt.value"
          :disabled="running || !status?.available"
          label="Check now"
          busy-label="Checking…"
          @click="check.run"
        />
        <button
          v-if="!auth.readOnly"
          type="button"
          class="btn-primary"
          :disabled="busy || running || !status?.available || !packages.length || !auth.isAdmin"
          :aria-busy="installing"
          @click="askInstall"
        >
          <LoaderCircle v-if="installing" class="size-4 animate-spin" aria-hidden="true" />
          {{ installLabel }}
        </button>
      </template>
      <div class="space-y-4">
        <p v-if="!poll.updatedAt.value" class="text-ink-muted">Reading…</p>
        <dl v-else class="kv max-w-md">
          <dt>System</dt>
          <dd>{{ status?.distro || 'unknown' }}</dd>
          <dt>Package manager</dt>
          <dd class="font-mono">{{ status?.manager || 'none found' }}</dd>
          <dt>Last checked</dt>
          <dd>{{ when(status?.lastCheck) }}</dd>
        </dl>

        <!-- These are part of the page rather than announcements, so they are
         plain text: a live region here would join the apply bar's. -->
        <AppNotice v-if="status && !status.available">{{ status.unavailable }}</AppNotice>
        <p v-else-if="!status && poll.updatedAt.value" class="text-ink-muted">
          Nothing on this router drives a package manager.
        </p>

        <p v-if="auth.isOperator" class="text-ink-muted">
          Only an admin can install updates, reboot, or change how updates run.
        </p>

        <fieldset v-if="config.draft" class="space-y-4" :disabled="!auth.isAdmin">
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
                : 'Package names or globs, comma separated: kernel* holds back every kernel package.'
            "
          >
            <input
              id="os-upd-exclude"
              v-model="excludes"
              class="input max-w-md font-mono"
              placeholder="kernel*, nvidia*"
            />
          </FormField>
        </fieldset>

        <p v-if="running" role="status" class="text-ink-muted">Updating. This can take a while.</p>

        <p v-if="status?.pending && !packages.length" class="text-ok">Everything is up to date.</p>

        <AppNotice v-if="status?.rebootRequired" kind="bad" title="This router wants a reboot">
          <p>{{ status.rebootReason }}</p>
          <ConfirmButton
            class="mt-1"
            label="Reboot now"
            question="Reboot this firewall?"
            description="Everything behind it loses its connection until it is back."
            confirm-label="Reboot"
            typed="reboot"
            :disabled="!auth.isAdmin"
            @confirm="reboot"
          />
        </AppNotice>

        <div v-if="status?.lastRun">
          <p class="text-ink-muted">
            Last run {{ when(status.lastRun)
            }}<span v-if="status.lastMode"> ({{ status.lastMode }})</span>
          </p>
          <p v-if="status.lastError" role="alert" class="text-bad">
            {{ status.lastError }}
          </p>
          <pre
            v-if="status.lastOutput"
            class="mt-1 max-h-48 overflow-auto rounded bg-page p-2 font-mono text-code whitespace-pre-wrap text-ink-2"
            >{{ status.lastOutput }}</pre>
        </div>

        <p v-if="status?.checkError" role="alert" class="text-bad">
          The last check failed: {{ status.checkError }}
        </p>
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      </div>
    </SectionCard>

    <SectionCard v-if="packages.length" title="Waiting" :count="packages.length" flush>
      <template #intro>
        <template v-if="securityCount">{{ securityCount }} of them security. </template>
        <template v-if="status?.pending?.note">{{ status.pending.note }}</template>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Package</th>
            <th>Installed</th>
            <th>Available</th>
            <th>From</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in shown" :key="p.name">
            <td>
              <span class="font-mono">{{ p.name }}</span>
              <span v-if="p.security" class="badge badge-warn ml-2">security</span>
            </td>
            <td class="font-mono text-code">{{ p.from || '—' }}</td>
            <td class="font-mono text-code">{{ p.to || '—' }}</td>
            <td class="text-ink-muted">{{ p.repo || '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="packages.length > SHOWN" class="card-strip border-t border-line">
        <button type="button" class="link" @click="showAll = !showAll">
          {{ showAll ? 'Show fewer' : `Show all ${packages.length}` }}
        </button>
      </div>
    </SectionCard>
  </div>
</template>
