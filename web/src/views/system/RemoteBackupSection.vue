<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, nextTick, ref, watch } from 'vue'

import AppDisclosure from '@/components/AppDisclosure.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes } from '@/lib/format'
import { SCHEDULE_PRESETS, presetFor } from '@/lib/schedules'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import RestorePreview from '@/views/system/RestorePreview.vue'

/** The derived cron that takes the copy; matches model.CronIDRemoteBackup. */
const CRON_ID = 'system:remote-backup'
const DEFAULT_SCHEDULE = '0 3 * * *'

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const status = ref(null)
const copies = ref([])
const pending = ref(null)
/** The copy whose own passphrase is being asked for, and the answer. */
const asking = ref('')
const rowPassphrase = ref('')

const settings = computed(() => config.remoteBackup)
const schedule = computed(() => settings.value.schedule || DEFAULT_SCHEDULE)
// The status and the copies follow the configuration that is applied:
// the cron reads that one, and a draft reaches no bucket.
const applied = computed(() => config.saved?.backup?.remote?.enabled === true)

function set(patch) {
  config.setRemoteBackup(patch)
}

/** The hostname as a copy's name has it, as backup.SafeHostname writes it. */
function safeHostname(name = '') {
  const host = name
    .replace(/[^a-zA-Z0-9._-]+/g, '-')
    .replace(/\.{2,}/g, '.')
    .replace(/^[-.]+|[-.]+$/g, '')
  return host || 'ostiole'
}

/**
 * Switching the copies on gives an empty Prefix a folder for this router,
 * filled in once: one that followed the hostname would lose sight of the
 * copies after a rename. A router already copying keeps its folder.
 */
function setEnabled(on) {
  const patch = { enabled: on }
  if (on && !applied.value && !settings.value.prefix?.trim()) {
    patch.prefix = `ostiole-${safeHostname(config.draft?.system?.hostname)}/`
  }
  set(patch)
}

const loadStatus = useAsync(async () => {
  const list = await api.crons.list()
  status.value = list.find((s) => s.id === CRON_ID) ?? null
})

const loadCopies = useAsync(async () => {
  copies.value = (await api.config.remoteCopies()).copies ?? []
})

const backUpNow = useAsync(async () => {
  const res = await api.crons.run(CRON_ID)
  if (res?.error) throw new Error(res.error)
  await loadStatus.run()
  await loadCopies.run()
})

const restore = useAsync(async (key, passphrase = '') => {
  try {
    pending.value = await api.config.restoreRemote(key, passphrase)
    asking.value = ''
    rowPassphrase.value = ''
  } catch (e) {
    // A copy locked with an older passphrase is the one case worth
    // asking about rather than reporting.
    if (e instanceof ApiError && e.status === 400 && /passphrase/i.test(e.message)) {
      asking.value = key
      return
    }
    throw e
  }
})

/** The copy whose restore is being read, for its row's spinner. */
const restoring = ref('')
let passphraseInput = null

async function restoreCopy(key) {
  restoring.value = key
  await restore.run(key, asking.value === key ? rowPassphrase.value : '')
  restoring.value = ''
  if (asking.value !== key) return
  await nextTick()
  passphraseInput?.focus()
}

/** Delete all, where a delete otherwise names one copy's key. */
const ALL = '*'
/** What is being deleted, for its spinner. */
const deleting = ref('')

// The page sends the keys it shows, so a copy uploaded since the list
// was read survives Delete all.
const remove = useAsync(async (what) => {
  try {
    if (what === ALL) await api.config.deleteAllRemote(copies.value.map((c) => c.key))
    else await api.config.deleteRemote(what)
  } finally {
    await loadCopies.run()
  }
})

async function deleteCopies(what) {
  deleting.value = what
  await remove.run(what)
  deleting.value = ''
}

const countOf = (n) => `${n} ${n === 1 ? 'copy' : 'copies'}`

/** The routers with copies in the folder and how many each has. */
const takers = computed(() => {
  const n = new Map()
  for (const c of copies.value) n.set(c.hostname, (n.get(c.hostname) ?? 0) + 1)
  return [...n]
})
const shared = computed(() => takers.value.length > 1)
/** "fw and fw2", "fw, fw2 and fw3". */
const sharers = computed(() => {
  const names = takers.value.map(([host]) => host)
  return `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`
})

watch(
  applied,
  (on) => {
    if (!on) return
    loadStatus.run()
    // Listing the bucket is an operator's, like restoring from it.
    if (!auth.readOnly) loadCopies.run()
  },
  { immediate: true },
)

const error = computed(() => backUpNow.error.value || restore.error.value || loadStatus.error.value)

async function loadIntoDraft() {
  if (
    config.dirty &&
    !(await confirm.ask({
      question: 'Replace the current draft?',
      description: 'Its unapplied changes are lost.',
      confirmLabel: 'Replace',
    }))
  )
    return
  config.replaceDraft(pending.value.config)
  pending.value = null
}

const when = (s) => (s ? new Date(s).toLocaleString() : 'never')
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Remote backup"
      intro="Encrypted before it leaves. The administrator accounts and their password hashes are
        inside."
    >
      <template #actions>
        <ToggleRow
          id="rb-enabled"
          :model-value="settings.enabled === true"
          variant="switch"
          label="Enabled"
          aria-label="Remote backup enabled"
          :disabled="!auth.isAdmin"
          @update:model-value="setEnabled"
        />
      </template>

      <div class="space-y-4">
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
        <p v-if="auth.isOperator" class="text-ink-muted">Only an admin can change these.</p>

        <template v-if="config.draft">
          <!-- A disabled fieldset greys out every field in it; the fold's
               own button stays outside, so the settings can still be read. -->
          <fieldset class="grid max-w-2xl min-w-0 gap-4 sm:grid-cols-2" :disabled="!auth.isAdmin">
            <FormField id="rb-endpoint" label="Endpoint" hint="HTTPS, no path.">
              <input
                id="rb-endpoint"
                class="input"
                placeholder="https://s3.us-west-004.backblazeb2.com"
                :value="settings.endpoint ?? ''"
                @change="set({ endpoint: $event.target.value.trim() })"
              />
            </FormField>
            <FormField id="rb-region" label="Region" hint="Empty reads it from the endpoint.">
              <input
                id="rb-region"
                class="input"
                :value="settings.region ?? ''"
                @change="set({ region: $event.target.value.trim() })"
              />
            </FormField>
            <FormField id="rb-bucket" label="Bucket">
              <input
                id="rb-bucket"
                class="input"
                :value="settings.bucket ?? ''"
                @change="set({ bucket: $event.target.value.trim() })"
              />
            </FormField>
            <FormField id="rb-key" label="Key ID">
              <input
                id="rb-key"
                class="input"
                autocomplete="off"
                :value="settings.keyId ?? ''"
                @change="set({ keyId: $event.target.value.trim() })"
              />
            </FormField>
            <FormField id="rb-secret" label="Secret">
              <input
                id="rb-secret"
                type="password"
                class="input"
                autocomplete="new-password"
                :value="settings.secret ?? ''"
                @change="set({ secret: $event.target.value })"
              />
            </FormField>
            <FormField
              id="rb-passphrase"
              label="Passphrase"
              hint="Locks every copy. Lost with it, the copies cannot be opened."
            >
              <input
                id="rb-passphrase"
                type="password"
                class="input"
                autocomplete="new-password"
                :value="settings.passphrase ?? ''"
                @change="set({ passphrase: $event.target.value })"
              />
            </FormField>
          </fieldset>

          <AppDisclosure>
            <fieldset class="min-w-0 space-y-4" :disabled="!auth.isAdmin">
              <FormField
                id="rb-prefix"
                label="Prefix"
                hint="One per router. Retention and Delete all touch only this folder."
                class="max-w-md"
              >
                <input
                  id="rb-prefix"
                  class="input"
                  placeholder="ostiole/"
                  :value="settings.prefix ?? ''"
                  @change="set({ prefix: $event.target.value.trim() })"
                />
              </FormField>

              <div class="form-row max-w-2xl">
                <FormField id="rb-preset" label="Take a copy">
                  <select
                    id="rb-preset"
                    class="input w-64 max-sm:w-full"
                    :value="presetFor(schedule)"
                    @change="$event.target.value && set({ schedule: $event.target.value })"
                  >
                    <option value="">Something else</option>
                    <option v-for="p in SCHEDULE_PRESETS" :key="p.value" :value="p.value">
                      {{ p.label }}
                    </option>
                  </select>
                </FormField>
                <FormField id="rb-schedule" label="Schedule" hint="Router time.">
                  <input
                    id="rb-schedule"
                    class="input w-48 font-mono max-sm:w-full"
                    :value="schedule"
                    :placeholder="DEFAULT_SCHEDULE"
                    @change="set({ schedule: $event.target.value })"
                  />
                </FormField>
              </div>

              <div class="form-row max-w-2xl">
                <FormField id="rb-keep" label="Keep copies" hint="0 keeps every copy.">
                  <input
                    id="rb-keep"
                    type="number"
                    min="0"
                    class="input w-32 max-sm:w-full"
                    :value="settings.keep ?? 0"
                    @change="set({ keep: Number($event.target.value) || 0 })"
                  />
                </FormField>
                <FormField
                  id="rb-days"
                  label="Days"
                  hint="0 writes no rule. Written to the bucket on each run."
                >
                  <input
                    id="rb-days"
                    type="number"
                    min="0"
                    class="input w-32 max-sm:w-full"
                    :value="settings.days ?? 0"
                    @change="set({ days: Number($event.target.value) || 0 })"
                  />
                </FormField>
              </div>
            </fieldset>
          </AppDisclosure>

          <p class="max-w-3xl text-ink-muted">
            The key needs write access. Keep copies also needs list and delete, and deleting needs
            them for every version. Restoring needs read, and Days needs bucket changes.
          </p>
        </template>
      </div>
    </SectionCard>

    <SectionCard
      v-if="applied"
      title="Copies"
      :count="copies.length"
      intro="Back up now uses the applied settings."
      flush
    >
      <template v-if="!auth.readOnly" #actions>
        <RefreshButton
          :busy="loadCopies.busy.value"
          :updated-at="loadCopies.updatedAt.value"
          @click="loadCopies.run"
        />
        <button
          type="button"
          class="btn-secondary"
          :disabled="backUpNow.busy.value"
          :aria-busy="backUpNow.busy.value"
          @click="backUpNow.run"
        >
          <LoaderCircle
            v-if="backUpNow.busy.value"
            class="size-4 animate-spin"
            aria-hidden="true"
          />
          {{ backUpNow.busy.value ? 'Uploading…' : 'Back up now' }}
        </button>
        <ConfirmButton
          v-if="copies.length"
          label="Delete all"
          busy-label="Deleting…"
          :busy="deleting === ALL"
          :disabled="!auth.isAdmin || remove.busy.value"
          :title="auth.isAdmin ? undefined : 'Only an admin can delete all copies.'"
          :question="copies.length === 1 ? 'Delete 1 copy?' : `Delete all ${copies.length} copies?`"
          description="Hidden copies in the folder go too. None can be restored after this."
          :typed="config.saved.backup.remote.bucket"
          :dependents="shared ? takers.map(([host, n]) => `${host}, ${countOf(n)}`) : []"
          dependents-label="Taken by"
          @confirm="deleteCopies(ALL)"
        />
      </template>

      <div class="card-strip space-y-3">
        <dl class="kv max-w-xl">
          <dt>Last upload</dt>
          <dd>{{ when(status?.lastRun) }}</dd>
          <dt>Result</dt>
          <dd v-if="status?.lastError" class="text-bad">{{ status.lastError }}</dd>
          <dd v-else>{{ status?.lastOutput || '—' }}</dd>
          <dt>Next</dt>
          <dd>{{ when(status?.next) }}</dd>
        </dl>
        <p v-if="shared" class="text-ink-muted">Copies from {{ sharers }} share this folder.</p>
        <p v-if="loadCopies.error.value" role="alert" class="text-bad">
          {{ loadCopies.error.value }}
        </p>
        <p v-if="remove.error.value" role="alert" class="text-bad">{{ remove.error.value }}</p>
      </div>

      <table v-if="!auth.readOnly" class="table table-stack">
        <thead>
          <tr>
            <th>Name</th>
            <th>Taken</th>
            <th>Size</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!copies.length">
            <td colspan="4" class="text-ink-muted">
              {{ loadCopies.updatedAt.value ? 'No copies.' : 'Reading…' }}
            </td>
          </tr>
          <tr v-for="c in copies" :key="c.key">
            <td class="font-mono text-code break-all" data-label="">{{ c.name }}</td>
            <td class="text-xs whitespace-nowrap" data-label="Taken">
              {{ new Date(c.takenAt).toLocaleString() }}
            </td>
            <td class="tabular-nums" data-label="Size">{{ formatBytes(c.size) }}</td>
            <td class="actions" data-label="">
              <input
                v-if="asking === c.key"
                :ref="(el) => (passphraseInput = el)"
                v-model="rowPassphrase"
                type="password"
                class="input mr-2 inline-block w-48 max-sm:w-full"
                autocomplete="off"
                :aria-label="`Passphrase for ${c.name}`"
                placeholder="Passphrase for this copy"
              />
              <button
                type="button"
                class="link-action"
                :disabled="restore.busy.value"
                :aria-busy="restoring === c.key"
                @click="restoreCopy(c.key)"
              >
                <LoaderCircle
                  v-if="restoring === c.key"
                  class="mr-1 inline size-4 animate-spin"
                  aria-hidden="true"
                />
                Restore
              </button>
              <ConfirmButton
                label="Delete"
                :busy="deleting === c.key"
                :disabled="remove.busy.value"
                :question="`Delete copy ${c.name}?`"
                description="It cannot be restored after this."
                @confirm="deleteCopies(c.key)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <RestorePreview
      v-if="pending"
      :pending="pending"
      @load="loadIntoDraft"
      @cancel="pending = null"
    />
  </div>
</template>
