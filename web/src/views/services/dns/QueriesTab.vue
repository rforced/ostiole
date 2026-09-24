<script setup>
import { LoaderCircle, Pause, Play } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { exceptionToggles, sentence } from '@/lib/blocking'
import { formatCount } from '@/lib/format'
import { streamLost } from '@/lib/stream'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

/** How many streamed rows to hold before the oldest go. */
const MAX_LIVE = 500

const auth = useAuthStore()
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

/**
 * Where each page read starts: the newest answers, then the ones older
 * than the last row of the page before. Only the page on screen is kept,
 * however far back the reading goes.
 */
const cursors = ref([{ before: 0, offset: 0 }])
const cursor = computed(() => cursors.value[cursors.value.length - 1])
const newest = computed(() => cursors.value.length === 1)

function read(c) {
  return Promise.all([
    api.queries.list(params(c.before ? { before: c.before } : {})),
    api.queries.summary(),
  ])
}

const load = useAsync(async () => {
  ;[page.value, summary.value] = await read(cursor.value)
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

const older = useAsync(async () => {
  const rows = page.value?.entries ?? []
  if (!rows.length) return
  const next = { before: rows[rows.length - 1].seq, offset: cursor.value.offset + rows.length }
  ;[page.value, summary.value] = await read(next)
  cursors.value = [...cursors.value, next]
  // What streams in is new, and this page is not.
  stopLive()
})

const newer = useAsync(async () => {
  const back = cursors.value.slice(0, -1)
  ;[page.value, summary.value] = await read(back[back.length - 1])
  cursors.value = back
})

const paging = computed(() => older.busy.value || newer.busy.value)
const hasOlder = computed(
  () => cursor.value.offset + (page.value?.entries ?? []).length < (page.value?.total ?? 0),
)

const clear = useAsync(async () => {
  await api.queries.clear()
  streamed.value = []
  cursors.value = [{ before: 0, offset: 0 }]
  await load.run()
})

/** The lists to offer in the filter, from what the router has applied. */
const loadLists = useAsync(async () => {
  lists.value = (await api.blocking.status()).lists ?? []
})

function connect() {
  const es = new EventSource('/api/v1/dns/queries/stream')
  source = es
  es.onopen = () => {
    streamError.value = ''
  }
  es.onmessage = (ev) => {
    try {
      const row = JSON.parse(ev.data)
      if (!matches(row)) return
      streamed.value = [{ ...row, key: ++key }, ...streamed.value].slice(0, MAX_LIVE)
    } catch {
      /* ignore malformed */
    }
  }
  es.onerror = () => {
    streamError.value = streamLost(es)
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

function stopLive() {
  if (live.value) toggleLive()
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

/** The exception lists, as their toggles call them. */
const EXCEPTIONS = { allow: 'Never block', deny: 'Always block' }

/** What each row offers to toggle, from the draft rather than the log. */
const toggleFor = computed(() => exceptionToggles(config.draft))

const rows = computed(() => {
  const toggle = toggleFor.value
  return [
    ...streamed.value,
    ...(page.value?.entries ?? []).map((e) => ({ ...e, key: `s${e.seq}` })),
  ].map((e) => ({ ...e, toggle: toggle(e) }))
})

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
  cursors.value = [{ before: 0, offset: 0 }]
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
      :locked="auth.readOnly"
    >
      <template #actions>
        <ToggleRow
          id="qlog-enabled"
          v-model="enabled"
          variant="switch"
          label="Enabled"
          aria-label="Query log enabled"
          :disabled="auth.readOnly"
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
            class="input w-32 max-sm:w-full"
          />
        </FormField>
        <FormField
          id="qlog-hours"
          label="Hours"
          hint="24 is the default, 720 a month. Older answers are dropped."
        >
          <input
            id="qlog-hours"
            v-model.number="hours"
            type="number"
            min="0"
            max="720"
            placeholder="24"
            class="input w-32 max-sm:w-full"
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
              class="input w-56 font-mono max-sm:w-full"
              spellcheck="false"
              placeholder="any"
            />
          </FormField>
          <FormField id="q-client" label="Client" hint="An address or a device name.">
            <input
              id="q-client"
              v-model="filter.client"
              class="input w-44 font-mono max-sm:w-full"
              spellcheck="false"
              placeholder="any"
            />
          </FormField>
          <FormField id="q-status" label="Status">
            <select id="q-status" v-model="filter.status" class="input w-40 max-sm:w-full">
              <option value="">Any</option>
              <option value="blocked">Blocked</option>
              <option value="ok">Answered</option>
              <option value="nxdomain">No such name</option>
              <option value="nodata">No data</option>
              <option value="servfail">Failed</option>
            </select>
          </FormField>
          <FormField id="q-list" label="List">
            <select id="q-list" v-model="filter.list" class="input w-40 max-sm:w-full">
              <option value="">Any</option>
              <option v-for="l in lists" :key="l.name" :value="l.name">{{ l.name }}</option>
            </select>
          </FormField>
          <FormField id="q-type" label="Type">
            <select id="q-type" v-model="filter.type" class="input w-28 max-sm:w-full">
              <option value="">Any</option>
              <option v-for="t in ['A', 'AAAA', 'HTTPS', 'PTR', 'SRV', 'TXT', 'MX']" :key="t">
                {{ t }}
              </option>
            </select>
          </FormField>
          <button type="button" class="btn-secondary" :disabled="!newest" @click="toggleLive">
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
        <!-- On a phone a query is two lines: when, what and its type; who asked
             and what they got, and its toggle at the end. -->
        <table class="table table-flow">
          <thead>
            <tr>
              <th>Time</th>
              <th>Client</th>
              <th>Name</th>
              <th>Type</th>
              <th>Status</th>
              <th>Answer</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!rows.length">
              <td colspan="7" class="text-ink-muted">
                {{
                  load.busy.value && !page ? 'Reading…' : newest ? 'No queries.' : 'Nothing older.'
                }}
              </td>
            </tr>
            <template v-for="e in rows" :key="e.key">
              <tr class="max-sm:after:order-4 max-sm:after:basis-full max-sm:after:content-['']">
                <td class="font-mono text-code whitespace-nowrap max-sm:order-1">
                  {{ new Date(e.time).toLocaleTimeString() }}
                </td>
                <td class="max-sm:order-5">
                  <template v-if="e.device">
                    {{ e.device }}
                    <div class="font-mono text-xs text-ink-muted">{{ e.client }}</div>
                  </template>
                  <span v-else class="font-mono text-code">{{ e.client }}</span>
                </td>
                <td class="font-mono text-code break-all max-sm:order-2 max-sm:font-medium">
                  {{ e.name }}
                </td>
                <td class="font-mono text-code max-sm:order-3 max-sm:text-ink-muted">
                  {{ e.type }}
                </td>
                <td class="max-sm:order-6">
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
                <td class="font-mono text-code max-sm:order-7">{{ e.answer }}</td>
                <td class="text-right whitespace-nowrap max-sm:order-8 max-sm:ml-auto">
                  <label
                    v-if="e.toggle && !auth.readOnly"
                    class="inline-flex items-center gap-2 max-sm:-my-3 max-sm:py-3"
                    :class="e.toggle.on ? 'text-ink' : 'text-ink-muted'"
                  >
                    <input
                      type="checkbox"
                      class="size-4 rounded"
                      :checked="e.toggle.on"
                      :aria-label="`${EXCEPTIONS[e.toggle.key]} ${e.name}`"
                      @change="config.setException(e.name, e.toggle.key, $event.target.checked)"
                    />
                    {{ EXCEPTIONS[e.toggle.key] }}
                  </label>
                </td>
              </tr>
              <tr v-if="why[e.name]">
                <td colspan="7" class="text-sm text-ink-muted">{{ why[e.name] }}</td>
              </tr>
            </template>
          </tbody>
        </table>
      </template>

      <div v-if="running" class="card-strip flex items-center gap-3 border-t border-line">
        <button
          v-if="!newest"
          type="button"
          class="btn-secondary"
          :disabled="paging"
          :aria-busy="newer.busy.value"
          @click="newer.run()"
        >
          <LoaderCircle v-if="newer.busy.value" class="size-4 animate-spin" aria-hidden="true" />
          Newer
        </button>
        <button
          v-if="hasOlder"
          type="button"
          class="btn-secondary"
          :disabled="paging"
          :aria-busy="older.busy.value"
          @click="older.run()"
        >
          <LoaderCircle v-if="older.busy.value" class="size-4 animate-spin" aria-hidden="true" />
          Older
        </button>
        <span v-if="page?.entries?.length" class="text-sm text-ink-muted">
          {{ formatCount(cursor.offset + 1) }}–{{
            formatCount(cursor.offset + page.entries.length)
          }}
          of {{ formatCount(page.total) }}.
        </span>
        <p v-if="older.error.value || newer.error.value" role="alert" class="text-sm text-bad">
          {{ older.error.value || newer.error.value }}
        </p>
      </div>
    </SectionCard>
  </div>
</template>
