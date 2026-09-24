<script setup>
import { LoaderCircle, Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import CronDialog from '@/views/system/crons/CronDialog.vue'
import { serviceName } from '@/views/system/crons/services'

const REFRESH_MS = 10_000

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const statuses = ref([])
const open = ref(false)
const editing = ref(null)

const load = useAsync(
  async () => {
    statuses.value = await api.crons.list()
  },
  { interval: REFRESH_MS },
)

onMounted(() => Promise.all([config.load(), load.run()]))

/** A cron only an admin may change: it runs as root, or writes password hashes out. */
const adminOnly = (c) => c.kind === 'command' || c.withUsers === true
const locked = (c) => !auth.isAdmin && adminOnly(c)

const byID = computed(() => Object.fromEntries(statuses.value.map((s) => [s.id, s])))
const system = computed(() => statuses.value.filter((s) => s.origin === 'system'))

/** Draft crons merged with what the daemon has actually been doing. */
const rows = computed(() =>
  (config.crons ?? []).map((c) => ({ ...c, status: byID.value[c.id] ?? null })),
)

/** One at a time; the outcome lands in the row once the list is read again. */
const runner = useAsync(async (c) => {
  const res = await api.crons.run(c.id)
  if (res?.error) throw new Error(`${c.id}: ${res.error}`)
  await load.run()
})

const error = computed(() => load.error.value || runner.error.value)
/** The cron whose Run now is in flight. */
const starting = ref('')

async function runNow(c) {
  const ok = await confirm.ask({
    question: `Run ${c.description || c.id} now?`,
    description: 'As root, with the saved configuration.',
    confirmLabel: 'Run',
  })
  if (!ok) return
  starting.value = c.id
  await runner.run(c)
  starting.value = ''
}

function add() {
  editing.value = null
  open.value = true
}
function edit(c) {
  editing.value = c
  open.value = true
}

const when = (s) => (s ? new Date(s).toLocaleString() : '—')

/** What a cron does, in the words of whoever has to read the page. */
function describe(c) {
  switch (c.kind) {
    case 'backup':
      return `Back up the configuration to ${c.directory}, keeping ${c.keep || 10}`
    case 'refresh-aliases':
      return 'Fetch the address lists and country ranges'
    case 'refresh-blocklists':
      return 'Fetch the DNS blocklists'
    case 'restart-service':
      return `Restart ${serviceName(c.service)}`
    case 'command':
      return [c.command, ...(c.args ?? [])].join(' ')
  }
  return c.kind
}
</script>

<template>
  <div class="space-y-5">
    <template v-if="config.draft">
      <SectionCard
        title="Your crons"
        :count="rows.length"
        intro="Run now uses the saved configuration, so apply a new cron before trying it."
        flush
      >
        <template #actions>
          <RefreshButton
            :busy="load.busy.value"
            :updated-at="load.updatedAt.value"
            @click="load.run"
          />
          <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="add">
            <Plus class="size-4" aria-hidden="true" /> Add cron
          </button>
        </template>
        <div v-if="error" class="card-strip">
          <p role="alert" class="text-bad">{{ error }}</p>
        </div>
        <table class="table">
          <thead>
            <tr>
              <th>Cron</th>
              <th>Schedule</th>
              <th>Next</th>
              <th>Last run</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!rows.length">
              <td colspan="5" class="text-ink-muted">No crons.</td>
            </tr>
            <tr
              v-for="c in rows"
              :key="c.id"
              :class="{
                'opacity-50': !c.enabled,
                'row-changed': config.isChanged('crons', c.id),
              }"
            >
              <td>
                <div class="font-medium">{{ c.description || c.kind }}</div>
                <div class="font-mono text-code break-all text-ink-muted">
                  {{ describe(c) }}
                </div>
              </td>
              <td class="font-mono text-code">
                {{ c.schedule }}
                <span v-if="!c.enabled" class="badge ml-1">off</span>
              </td>
              <td class="text-xs">{{ c.enabled ? when(c.status?.next) : '—' }}</td>
              <td class="text-xs">
                <template v-if="c.status?.running">
                  <span class="badge">running</span>
                </template>
                <template v-else-if="c.status?.lastRun">
                  {{ when(c.status.lastRun) }}
                  <span v-if="c.status.lastError" class="badge badge-warn ml-1">failed</span>
                  <div
                    v-if="c.status.lastError"
                    class="mt-1 font-mono text-code break-all text-bad"
                  >
                    {{ c.status.lastError }}
                  </div>
                  <div
                    v-else-if="c.status.lastOutput"
                    class="mt-1 font-mono text-code break-all text-ink-muted"
                  >
                    {{ c.status.lastOutput }}
                  </div>
                </template>
                <span v-else class="text-ink-muted">never</span>
              </td>
              <td class="text-right whitespace-nowrap">
                <button
                  v-if="!auth.readOnly"
                  type="button"
                  class="link mr-3"
                  :disabled="
                    runner.busy.value ||
                    c.status?.running ||
                    (c.kind === 'command' && !auth.isAdmin)
                  "
                  :aria-busy="starting === c.id || c.status?.running === true"
                  @click="runNow(c)"
                >
                  <LoaderCircle
                    v-if="starting === c.id || c.status?.running"
                    class="mr-1 inline size-4 animate-spin"
                    aria-hidden="true"
                  />
                  Run now
                </button>
                <button
                  type="button"
                  class="link"
                  :disabled="locked(c) && !auth.readOnly"
                  @click="edit(c)"
                >
                  {{ auth.readOnly ? 'View' : 'Edit' }}
                </button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete cron ${c.description || c.id}?`"
                  :disabled="locked(c)"
                  @confirm="config.removeCron(c.id)"
                />
              </td>
            </tr>
          </tbody>
        </table>
        <div
          v-if="auth.isOperator && rows.some(locked)"
          class="card-strip border-t border-line text-ink-muted"
        >
          Only an admin can change crons that run a command or back up accounts.
        </div>
      </SectionCard>
    </template>

    <SectionCard title="Ostiole's crons" :count="system.length" flush>
      <table class="table">
        <thead>
          <tr>
            <th>Work</th>
            <th>How often</th>
            <th>Last seen</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!system.length">
            <td colspan="3" class="text-ink-muted">
              {{
                load.busy.value && !load.updatedAt.value
                  ? 'Reading…'
                  : 'Nothing is reporting. This needs the daemon.'
              }}
            </td>
          </tr>
          <tr v-for="s in system" :key="s.id">
            <td>{{ s.description }}</td>
            <!-- An update mode of manual turns its install off; saying
                   when it would have run would be a lie. -->
            <td class="font-mono text-code">{{ s.enabled ? s.schedule : 'never' }}</td>
            <td class="text-xs">{{ when(s.lastRun) }}</td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <CronDialog v-model:open="open" :cron="editing" />
  </div>
</template>
