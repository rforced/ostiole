<script setup>
import { Plus, RefreshCw } from 'lucide-vue-next'
import { computed, onMounted, onUnmounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import CronDialog from '@/views/crons/CronDialog.vue'

const config = useConfigStore()
const statuses = ref([])
const error = ref('')
const open = ref(false)
const editing = ref(null)
const running = ref('')
let timer = null

onMounted(async () => {
  await Promise.all([config.load(), refresh()])
  timer = setInterval(refresh, 10_000)
})
onUnmounted(() => clearInterval(timer))

async function refresh() {
  try {
    statuses.value = await api.crons.list()
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

const byID = computed(() => Object.fromEntries(statuses.value.map((s) => [s.id, s])))
const system = computed(() => statuses.value.filter((s) => s.kind === 'system'))

/** Draft jobs merged with what the daemon has actually been doing. */
const jobs = computed(() =>
  (config.crons ?? []).map((c) => ({ ...c, status: byID.value[c.id] ?? null })),
)

async function runNow(id) {
  running.value = id
  try {
    const res = await api.crons.run(id)
    if (res?.error) error.value = `${id}: ${res.error}`
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    running.value = ''
  }
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

/** What a job does, in the words of whoever has to read the page. */
function describe(c) {
  switch (c.job) {
    case 'backup':
      return `Back up the configuration to ${c.directory}, keeping ${c.keep || 10}`
    case 'refresh-aliases':
      return 'Fetch the blocklists and country ranges'
    case 'restart-service':
      return `Restart ${c.service}`
    case 'command':
      return [c.command, ...(c.args ?? [])].join(' ')
  }
  return c.job
}
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Crons</h1>
    <p class="max-w-3xl text-sm text-neutral-500">
      What this box does while nobody is watching. Your own jobs are below, and under them the work
      Ostiole does on its own account, so the whole answer is on one page.
    </p>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <template v-if="config.draft">
      <section class="space-y-3" aria-labelledby="crons-title">
        <div class="flex items-center gap-3">
          <h2 id="crons-title" class="font-medium">Your jobs</h2>
          <button type="button" class="btn-secondary" @click="add">
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add job
          </button>
          <button type="button" class="btn-secondary" @click="refresh">
            <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Refresh
          </button>
        </div>
        <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
          <table class="table">
            <thead>
              <tr>
                <th>Job</th>
                <th>Schedule</th>
                <th>Next</th>
                <th>Last run</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!jobs.length">
                <td colspan="5" class="text-neutral-500">
                  No jobs yet. A nightly backup is the one most boxes want.
                </td>
              </tr>
              <tr v-for="c in jobs" :key="c.id" :class="{ 'opacity-50': !c.enabled }">
                <td>
                  <div class="font-medium">{{ c.description || c.job }}</div>
                  <div class="font-mono text-xs break-all text-neutral-500">{{ describe(c) }}</div>
                </td>
                <td class="font-mono text-xs">
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
                      v-if="c.status.lastError || c.status.lastOutput"
                      class="mt-1 font-mono text-xs break-all text-neutral-500"
                    >
                      {{ c.status.lastError || c.status.lastOutput }}
                    </div>
                  </template>
                  <span v-else class="text-neutral-500">never</span>
                </td>
                <td class="text-right whitespace-nowrap">
                  <button
                    type="button"
                    class="link mr-3"
                    :disabled="running !== ''"
                    @click="runNow(c.id)"
                  >
                    Run now
                  </button>
                  <button type="button" class="link" @click="edit(c)">Edit</button>
                  <ConfirmButton
                    class="ml-3"
                    label="Delete"
                    confirm-label="Delete job?"
                    @confirm="config.removeCron(c.id)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="text-sm text-neutral-500">
          A job runs as root on this box. "Run now" uses whatever is saved, so apply a new job
          before trying it.
        </p>
      </section>
    </template>

    <section class="space-y-3" aria-labelledby="system-title">
      <h2 id="system-title" class="font-medium">What Ostiole does by itself</h2>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
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
              <td colspan="3" class="text-neutral-500">
                Nothing is reporting; this needs the daemon.
              </td>
            </tr>
            <tr v-for="s in system" :key="s.id">
              <td>{{ s.description }}</td>
              <td class="font-mono text-xs">{{ s.schedule }}</td>
              <td class="text-xs">{{ when(s.lastRun) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <CronDialog v-model:open="open" :cron="editing" />
  </div>
</template>
