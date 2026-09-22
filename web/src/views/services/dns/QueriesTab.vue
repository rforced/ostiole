<script setup>
import { Pause, Play } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { sentence } from '@/lib/blocking'
import { formatCount } from '@/lib/format'
import { useConfigStore } from '@/stores/config'

/** How many streamed rows to hold before the oldest go. */
const MAX_LIVE = 500

const config = useConfigStore()
const dns = computed(() => config.ensureServices().dns)

/** The switch, kept out of the model when off. */
const queryLog = computed(() => dns.value.queryLog ?? {})
function setQueryLog(q) {
  if (Object.keys(q).length) dns.value.queryLog = q
  else delete dns.value.queryLog
}
const enabled = computed({
  get: () => Boolean(queryLog.value.enabled),
  set: (on) => {
    const q = { ...queryLog.value }
    if (on) q.enabled = true
    else delete q.enabled
    setQueryLog(q)
  },
})
/** Empty means the default, which the placeholder shows. */
function numberField(key) {
  return computed({
    get: () => queryLog.value[key] || '',
    set: (v) => {
      const q = { ...queryLog.value }
      if (Number.isFinite(v) && v > 0) q[key] = v
      else delete q[key]
      setQueryLog(q)
    },
  })
}
const entries = numberField('entries')
const hours = numberField('hours')

/** What the chosen ceiling costs once the log is full; it grows into it. */
const entriesMB = computed(() =>
  Math.round(((entries.value || 20000) * 150) / (1024 * 1024)).toLocaleString(),
)

// Below the card everything reads the router, not the draft: the log is
// what has happened, and a setting that has not been applied has not.
const page = ref(null)
const summary = ref(null)
const lists = ref([])
const filter = ref({ name: '', client: '', status: '', list: '', type: '' })
const live = ref(false)
const streamed = ref([])
const streamError = ref('')
const why = ref({})
let source = null
let key = 0

const load = useAsync(async () => {
  const [p, s] = await Promise.all([api.queries.list(params()), api.queries.summary()])
  page.value = p
  summary.value = s
})

function params(extra = {}) {
  return {
    name: filter.value.name.trim(),
    client: filter.value.client.trim(),
    status: filter.value.status,
    list: filter.value.list,
    type: filter.value.type,
    ...extra,
  }
}

const more = useAsync(async () => {
  const rows = page.value?.entries ?? []
  if (!rows.length) return
  const next = await api.queries.list(params({ before: rows[rows.length - 1].seq }))
  page.value = { ...next, entries: [...rows, ...next.entries] }
})

const clear = useAsync(async () => {
  await api.queries.clear()
  streamed.value = []
  await load.run()
})

/** The lists to offer in the filter, from what the router has applied. */
const loadLists = useAsync(async () => {
  lists.value = (await api.blocking.status()).lists ?? []
})

function connect() {
  source = new EventSource('/api/v1/dns/queries/stream')
  source.onmessage = (ev) => {
    try {
      const row = JSON.parse(ev.data)
      if (!matches(row)) return
      streamed.value = [{ ...row, key: ++key }, ...streamed.value].slice(0, MAX_LIVE)
    } catch {
      /* ignore malformed */
    }
  }
  source.onerror = () => {
    if (!streamError.value) streamError.value = 'Stream disconnected, retrying…'
  }
}

function disconnect() {
  source?.close()
  source = null
  streamError.value = ''
}

function toggleLive() {
  live.value = !live.value
  if (live.value) connect()
  else {
    disconnect()
    streamed.value = []
  }
}

/** The filter, applied to a streamed row the server did not filter. */
function matches(row) {
  const f = filter.value
  if (f.name && !row.name?.toLowerCase().includes(f.name.trim().toLowerCase())) return false
  if (f.status && row.status !== f.status) return false
  if (f.type && row.type !== f.type) return false
  if (f.list && !(row.lists ?? []).includes(f.list)) return false
  if (f.client) {
    const q = f.client.trim().toLowerCase()
    if (!row.client?.toLowerCase().includes(q) && !row.device?.toLowerCase().includes(q))
      return false
  }
  return true
}

const rows = computed(() => [
  ...streamed.value,
  ...(page.value?.entries ?? []).map((e) => ({ ...e, key: `s${e.seq}` })),
])

/** Nothing was ever read: the daemon is not root, or the router is away. */
const unreadable = computed(() => Boolean(load.error.value) && !page.value)
const running = computed(() => Boolean(page.value?.enabled))
const dnsOn = computed(() => Boolean(config.draft?.services?.dns?.enabled))

const blockedPct = computed(() => {
  const s = summary.value
  if (!s?.total) return 0
  return Math.round((s.blocked / s.total) * 100)
})

const since = computed(() =>
  summary.value?.since ? new Date(summary.value.since).toLocaleString() : '',
)

/** Asks the router why a name is blocked, and keeps the sentence. A
 * second click puts it away again. */
const lookup = useAsync(async (name) => {
  why.value = { ...why.value, [name]: 'Looking…' }
  const finding = await api.blocking.lookup(name)
  why.value = { ...why.value, [name]: sentence(finding) }
})

function explain(name) {
  if (why.value[name]) {
    const next = { ...why.value }
    delete next[name]
    why.value = next
    return
  }
  lookup.run(name)
}

function search() {
  streamed.value = []
  load.run()
}

onMounted(() => {
  load.run()
  loadLists.run()
})
onBeforeUnmount(disconnect)
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Query log"
      intro="Kept in memory on this router only. Switching it off, or a restart, clears it."
    >
      <template #actions>
        <ToggleRow
          id="qlog-enabled"
          v-model="enabled"
          variant="switch"
          label="Enabled"
          aria-label="Query log enabled"
        />
      </template>
      <div v-if="enabled" class="grid max-w-2xl gap-4 sm:grid-cols-2">
        <FormField
          id="qlog-entries"
          label="Entries"
          :hint="`20000 is the default. About ${entriesMB} MB of memory when full.`"
        >
          <input
            id="qlog-entries"
            v-model.number="entries"
            type="number"
            min="0"
            max="10000000"
            placeholder="20000"
            class="input w-32"
          />
        </FormField>
        <FormField
          id="qlog-hours"
          label="Hours"
          hint="24 is the default. Older answers are dropped; 720 is a month."
        >
          <input
            id="qlog-hours"
            v-model.number="hours"
            type="number"
            min="0"
            max="720"
            placeholder="24"
            class="input w-32"
          />
        </FormField>
      </div>
      <p v-else class="text-ink-muted">Off.</p>
    </SectionCard>

    <p v-if="!dnsOn" class="text-sm text-ink-muted">The DNS server is off.</p>
    <p v-else-if="unreadable" role="alert" class="text-sm text-bad">
      {{ load.error.value }}
    </p>
    <SectionCard v-else title="Queries" flush>
      <template #intro>
        <template v-if="summary?.total">
          {{ formatCount(summary.total) }} answers since {{ since }} ·
          {{ formatCount(summary.blocked) }} blocked ({{ blockedPct }}%) ·
          {{ formatCount(summary.clients) }} clients
          <span v-if="summary.dropped"> · {{ formatCount(summary.dropped) }} dropped</span>
        </template>
      </template>
      <div class="card-strip">
        <!-- No submit button, so Enter in a field is wired by hand. -->
        <form class="form-row" @submit.prevent="search" @keydown.enter.prevent="search">
          <FormField id="q-name" label="Name">
            <input
              id="q-name"
              v-model="filter.name"
              class="input w-56 font-mono"
              spellcheck="false"
              placeholder="any"
            />
          </FormField>
          <FormField id="q-client" label="Client" hint="An address or a device name.">
            <input
              id="q-client"
              v-model="filter.client"
              class="input w-44 font-mono"
              spellcheck="false"
              placeholder="any"
            />
          </FormField>
          <FormField id="q-status" label="Status">
            <select id="q-status" v-model="filter.status" class="input w-40">
              <option value="">Any</option>
              <option value="blocked">Blocked</option>
              <option value="ok">Answered</option>
              <option value="nxdomain">No such name</option>
              <option value="nodata">No data</option>
              <option value="servfail">Failed</option>
            </select>
          </FormField>
          <FormField id="q-list" label="List">
            <select id="q-list" v-model="filter.list" class="input w-40">
              <option value="">Any</option>
              <option v-for="l in lists" :key="l.name" :value="l.name">{{ l.name }}</option>
            </select>
          </FormField>
          <FormField id="q-type" label="Type">
            <select id="q-type" v-model="filter.type" class="input w-28">
              <option value="">Any</option>
              <option v-for="t in ['A', 'AAAA', 'HTTPS', 'PTR', 'SRV', 'TXT', 'MX']" :key="t">
                {{ t }}
              </option>
            </select>
          </FormField>
          <button type="button" class="btn-secondary" @click="toggleLive">
            <component :is="live ? Pause : Play" class="size-4" aria-hidden="true" />
            {{ live ? 'Stop' : 'Live' }}
          </button>
          <RefreshButton
            :busy="load.busy.value"
            :updated-at="load.updatedAt.value"
            @click="search"
          />
          <ConfirmButton
            v-if="running"
            label="Clear"
            question="Clear the query log?"
            description="Every answer it holds, and the counts per list, are dropped."
            @confirm="clear.run()"
          />
        </form>
        <p
          v-if="load.error.value || clear.error.value || streamError"
          role="alert"
          class="mt-2 text-bad"
        >
          {{ load.error.value || clear.error.value || streamError }}
        </p>
      </div>

      <p v-if="page && !running" class="card-strip border-t border-line text-ink-muted">
        The log is off.
      </p>
      <template v-else>
        <table class="table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Client</th>
              <th>Name</th>
              <th>Type</th>
              <th>Status</th>
              <th>Answer</th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!rows.length" key="empty" class="row-static">
              <td colspan="6" class="text-ink-muted">
                {{ load.busy.value && !page ? 'Reading…' : 'No queries.' }}
              </td>
            </tr>
            <template v-for="e in rows" :key="e.key">
              <tr>
                <td class="font-mono text-code whitespace-nowrap">
                  {{ new Date(e.time).toLocaleTimeString() }}
                </td>
                <td>
                  <template v-if="e.device">
                    {{ e.device }}
                    <div class="font-mono text-xs text-ink-muted">{{ e.client }}</div>
                  </template>
                  <span v-else class="font-mono text-code">{{ e.client }}</span>
                </td>
                <td class="font-mono text-code break-all">{{ e.name }}</td>
                <td class="font-mono text-code">{{ e.type }}</td>
                <td>
                  <span class="badge" :class="{ 'badge-warn': e.status === 'blocked' }">
                    {{ e.status }}
                  </span>
                  <div v-if="e.lists?.length" class="font-mono text-xs text-ink-muted">
                    {{ e.lists.join(', ') }}
                  </div>
                  <button
                    v-if="e.status === 'blocked'"
                    type="button"
                    class="link"
                    @click="explain(e.name)"
                  >
                    Why?
                  </button>
                </td>
                <td class="font-mono text-code">{{ e.answer }}</td>
              </tr>
              <tr v-if="why[e.name]" :key="`${e.key}-why`" class="row-static">
                <td colspan="6" class="text-sm text-ink-muted">{{ why[e.name] }}</td>
              </tr>
            </template>
          </TransitionGroup>
        </table>
      </template>

      <div v-if="running" class="card-strip flex items-center gap-3 border-t border-line">
        <button
          v-if="(page?.entries ?? []).length < (page?.total ?? 0)"
          type="button"
          class="btn-secondary"
          :disabled="more.busy.value"
          @click="more.run()"
        >
          {{ more.busy.value ? 'Loading…' : 'Load more' }}
        </button>
        <span v-if="page?.total" class="text-sm text-ink-muted">
          Showing {{ formatCount((page.entries ?? []).length) }} of {{ formatCount(page.total) }}.
        </span>
      </div>
    </SectionCard>
  </div>
</template>
