<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import CronDialog from '@/views/crons/CronDialog.vue'

const REFRESH_MS = 10_000

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

async function runNow(c) {
  const ok = await confirm.ask({
    question: `Run ${c.description || c.id} now?`,
    description: 'As root, with the saved configuration.',
    confirmLabel: 'Run',
  })
  if (!ok) return
  await runner.run(c)
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
      return `Restart ${c.service}`
    case 'command':
      return [c.command, ...(c.args ?? [])].join(' ')
  }
  return c.kind
}
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Crons</h1>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <template v-if="config.draft">
      <section class="space-y-3" aria-labelledby="crons-title">
        <div class="flex items-center gap-3">
          <h2 id="crons-title" class="section-title">Your crons</h2>
          <button type="button" class="btn-secondary" @click="add">
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add cron
          </button>
          <RefreshButton
            :busy="load.busy.value"
            :updated-at="load.updatedAt.value"
            @click="load.run"
          />
        </div>
        <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
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
            <TransitionGroup name="row" tag="tbody">
              <tr v-if="!rows.length" key="empty" class="row-static">
                <td colspan="5" class="text-neutral-500">No crons yet.</td>
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
                  <div class="font-mono text-code break-all text-neutral-500">
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
                      class="mt-1 font-mono text-code break-all text-red-600 dark:text-red-400"
                    >
                      {{ c.status.lastError }}
                    </div>
                    <div
                      v-else-if="c.status.lastOutput"
                      class="mt-1 font-mono text-code break-all text-neutral-500"
                    >
                      {{ c.status.lastOutput }}
                    </div>
                  </template>
                  <span v-else class="text-neutral-500">never</span>
                </td>
                <td class="text-right whitespace-nowrap">
                  <button
                    type="button"
                    class="link mr-3"
                    :disabled="runner.busy.value"
                    @click="runNow(c)"
                  >
                    Run now
                  </button>
                  <button type="button" class="link" @click="edit(c)">Edit</button>
                  <ConfirmButton
                    class="ml-3"
                    label="Delete"
                    :question="`Delete cron ${c.description || c.id}?`"
                    @confirm="config.removeCron(c.id)"
                  />
                </td>
              </tr>
            </TransitionGroup>
          </table>
        </div>
        <p class="text-sm text-neutral-500">
          Run now uses the saved configuration, so apply a new cron before trying it.
        </p>
      </section>
    </template>

    <section class="space-y-3" aria-labelledby="system-title">
      <h2 id="system-title" class="section-title">What Ostiole does by itself</h2>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Work</th>
              <th>How often</th>
              <th>Last seen</th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!system.length" key="empty" class="row-static">
              <td colspan="3" class="text-neutral-500">
                {{
                  load.busy.value && !load.updatedAt.value
                    ? 'Reading…'
                    : 'Nothing is reporting. This needs the daemon.'
                }}
              </td>
            </tr>
            <tr v-for="s in system" :key="s.id">
              <td>{{ s.description }}</td>
              <td class="font-mono text-code">{{ s.schedule }}</td>
              <td class="text-xs">{{ when(s.lastRun) }}</td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>

    <CronDialog v-model:open="open" :cron="editing" />
  </div>
</template>
