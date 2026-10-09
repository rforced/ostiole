<script setup>
import { computed, ref } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { parseList } from '@/lib/lists'
import { excluded } from '@/lib/packages'
import { adminOnly, useAuthStore } from '@/stores/auth'
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
/** The mode the radios show, or the one in force before the draft is read. */
const mode = computed(
  () =>
    settings.value.mode ||
    (config.draft ? status.value?.defaultMode : status.value?.mode) ||
    'security',
)
/**
 * Disabled leaves the packages alone, and nothing refreshes what the last
 * check and install found, so the page shows none of it.
 */
const disabled = computed(() => mode.value === 'disabled')
const packages = computed(() => status.value?.pending?.packages ?? [])
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

/** What the mode installs, which is what the Waiting card lists. */
const waiting = computed(() => {
  if (disabled.value) return []
  return installSecurityOnly.value ? packages.value.filter((p) => p.security) : packages.value
})
const shown = computed(() => (showAll.value ? waiting.value : waiting.value.slice(0, SHOWN)))
/** Whether a package is on Never upgrade: listed, but left alone by Install. */
const neverUpgrade = (p) => excluded(p.name, settings.value.exclude)
/** What Install installs: the Waiting list less what is never upgraded. */
const installable = computed(() => waiting.value.filter((p) => !neverUpgrade(p)))

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
  // Never upgrade goes as the page shows it, applied or not, like the mode.
  const exclude = config.draft ? (settings.value.exclude ?? []) : undefined
  status.value = await api.systemUpdates.apply(installSecurityOnly.value, exclude)
  poll.start()
})

const busy = computed(() => check.busy.value || install.busy.value)
const error = computed(() => actionError.value || check.error.value || install.error.value)
const installing = computed(() => install.busy.value || running.value)
const installLabel = computed(() =>
  installSecurityOnly.value ? 'Install security updates' : 'Install all updates',
)

async function askInstall() {
  const n = installable.value.length
  const ok = await confirm.ask({
    question: `Install ${n} ${n === 1 ? 'update' : 'updates'}?`,
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
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Operating system updates"
      intro="The Linux underneath Ostiole, updated through its own package manager."
    >
      <template v-if="!auth.readOnly && !disabled" #actions>
        <RefreshButton
          :busy="check.busy.value"
          :updated-at="check.updatedAt.value"
          :disabled="running || !status?.available"
          label="Check now"
          busy-label="Checking…"
          @click="check.run"
        />
        <ActionButton
          kind="primary"
          :label="installLabel"
          busy-label="Installing…"
          :busy="installing"
          :disabled="busy || running || !status?.available || !installable.length || !auth.isAdmin"
          @click="askInstall"
        />
      </template>
      <div class="space-y-4">
        <p v-if="!poll.updatedAt.value" class="text-ink-muted">Reading…</p>
        <dl v-else class="kv">
          <dt>System</dt>
          <dd>{{ status?.distro || 'unknown' }}</dd>
          <dt>Package manager</dt>
          <dd class="font-mono">{{ status?.manager || 'none found' }}</dd>
          <dt>Last checked</dt>
          <dd>{{ formatWhen(status?.lastCheck, 'never') }}</dd>
        </dl>

        <!-- These are part of the page rather than announcements, so they are
         plain text: a live region here would join the apply bar's. -->
        <AppNotice v-if="status && !status.available">{{ status.unavailable }}</AppNotice>
        <p v-else-if="!status && poll.updatedAt.value" class="text-ink-muted">
          Nothing on this router drives a package manager.
        </p>

        <fieldset v-if="config.draft" class="space-y-4" :disabled="!auth.isAdmin">
          <UpdateModeFields
            prefix="os-upd"
            :hint="
              auth.isOperator
                ? adminOnly('install updates, reboot, or change', 'how updates run')
                : ''
            "
            :mode="settings.mode ?? ''"
            :default-mode="status?.defaultMode || 'security'"
            :check-schedule="settings.checkSchedule ?? ''"
            :install-schedule="settings.installSchedule ?? ''"
            :security-capable="status?.securityCapable !== false"
            :security-note="`${status?.manager ?? 'This package manager'} has no security-only channel.`"
            disabled-note="Nothing is checked or installed, on a schedule or by hand."
            @update:mode="config.setUpdates('system', { mode: $event })"
            @update:check-schedule="config.setUpdates('system', { checkSchedule: $event })"
            @update:install-schedule="config.setUpdates('system', { installSchedule: $event })"
          />
          <FormField
            id="os-upd-exclude"
            label="Never upgrade"
            hint="Package names or globs, comma separated: kernel* holds back every kernel package."
          >
            <input id="os-upd-exclude" v-model="excludes" class="input max-w-md font-mono" />
          </FormField>
        </fieldset>

        <p v-if="running" role="status" class="text-ink-muted">Updating. This can take a while.</p>

        <template v-if="!disabled">
          <!-- Only a check that answered can say nothing is waiting. -->
          <p v-if="status?.lastCheck && !status.checkError && !waiting.length" class="text-ok">
            {{ packages.length ? 'No security updates are waiting.' : 'Everything is up to date.' }}
          </p>

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
              Last run {{ formatWhen(status.lastRun)
              }}<span v-if="status.lastMode"> ({{ status.lastMode }})</span>
            </p>
            <ErrorLine v-if="status.lastError">
              {{ status.lastError }}
            </ErrorLine>
            <pre
              v-if="status.lastOutput"
              class="mt-1 max-h-48 overflow-auto rounded bg-page p-2 font-mono text-code whitespace-pre-wrap text-ink-2"
              >{{ status.lastOutput }}</pre>
          </div>

          <ErrorLine v-if="status?.checkError">
            The last check failed: {{ status.checkError }}
          </ErrorLine>
          <ErrorLine v-if="error">{{ error }}</ErrorLine>
        </template>
      </div>
    </SectionCard>

    <SectionCard v-if="waiting.length" title="Waiting" :count="waiting.length" flush>
      <template #intro>
        <template v-if="securityCount && !installSecurityOnly"
          >{{ securityCount }} of them security.
        </template>
        <template v-if="status?.pending?.note">{{ status.pending.note }}</template>
      </template>
      <table class="table table-stack">
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
            <td data-label="">
              <span class="inline-flex items-center gap-1.5">
                <span class="font-mono">{{ p.name }}</span>
                <span v-if="p.security" class="badge badge-warn">security</span>
                <span v-if="neverUpgrade(p)" class="badge">never upgrade</span>
              </span>
            </td>
            <td class="font-mono text-code" data-label="Installed">{{ p.from || '—' }}</td>
            <td class="font-mono text-code" data-label="Available">{{ p.to || '—' }}</td>
            <td class="text-ink-muted" data-label="From">{{ p.repo || '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="waiting.length > SHOWN" class="card-strip border-t border-line">
        <button type="button" class="link-action" @click="showAll = !showAll">
          {{ showAll ? 'Show fewer' : `Show all ${waiting.length}` }}
        </button>
      </div>
    </SectionCard>
  </div>
</template>
