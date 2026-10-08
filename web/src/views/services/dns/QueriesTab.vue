<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { exceptionToggles, sentence } from '@/lib/blocking'
import { formatCount, formatWhen } from '@/lib/format'
import { heldLine, keptLine, useLog } from '@/lib/log'
import { queryValues } from '@/lib/queries'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

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
/** Entries and days, beside the switch. */
const retention = computed({
  get: () => {
    const rest = { ...queryLog.value }
    delete rest.enabled
    return rest
  },
  set: (v) => setQueryLog(queryLog.value.enabled ? { enabled: true, ...v } : { ...v }),
})

// Below the card everything reads the router, not the draft: the log is
// what has happened, and a setting that has not been applied has not.
const summary = ref(null)
const lists = ref([])
const filter = ref({ status: '', list: '', type: '' })
const why = ref({})
const card = ref(null)

const log = useLog({
  // The summary is read with the first page, so the line above the table
  // counts what the table starts from.
  read: async (params, signal) => {
    const [page, s] = await Promise.all([
      api.queries.list(params, signal),
      params.before ? null : api.queries.summary(),
    ])
    if (s) summary.value = s
    return page
  },
  stream: '/api/v1/dns/queries/stream',
  filters: () => {
    const f = filter.value
    const out = {}
    for (const key of ['status', 'list', 'type']) if (f[key]) out[key] = f[key]
    return out
  },
  keep: (row) => {
    const f = filter.value
    if (f.status && row.status !== f.status) return false
    if (f.type && row.type !== f.type) return false
    if (f.list && !(row.lists ?? []).includes(f.list)) return false
    return true
  },
  values: queryValues,
  top: card,
})
const { query, live } = log

const clear = useAsync(async () => {
  await api.queries.clear()
  await log.reload()
})

/** The lists to offer in the filter, from what the router has applied. */
const loadLists = useAsync(
  async () => {
    lists.value = (await api.blocking.status()).lists ?? []
  },
  { immediate: true },
)

/** The exception lists, as their toggles call them. */
const EXCEPTIONS = { allow: 'Allow', deny: 'Block' }

/** What each row offers to toggle, from the draft rather than the log. */
const toggleFor = computed(() => exceptionToggles(config.draft))

const rows = computed(() => {
  const toggle = toggleFor.value
  return log.rows.value.map((e) => ({ ...e, toggle: toggle(e) }))
})

/** Written to files as well, as the draft has it. */
const filesOn = computed(() => Boolean(config.draft?.system?.logging?.files?.enabled))
/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

/** Nothing was ever read: the daemon is not root, or the router is away. */
const unreadable = computed(() => Boolean(log.error.value) && !log.page.value)
const running = computed(() => Boolean(log.page.value?.enabled))
const dnsOn = computed(() => Boolean(config.draft?.services?.dns?.enabled))
const error = computed(
  () => log.error.value || clear.error.value || log.streamError.value || loadLists.error.value,
)

const blockedPct = computed(() => {
  const s = summary.value
  if (!s?.total) return 0
  return Math.round((s.blocked / s.total) * 100)
})

const empty = computed(() => {
  if (!log.updatedAt.value) return 'Reading…'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return Object.values(filter.value).some(Boolean) ? 'Nothing matches this view.' : 'No queries.'
})

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
</script>

<template>
  <div class="space-y-5">
    <LogRetention
      v-model="retention"
      log="queries"
      title="Query log"
      :intro="keptLine(filesOn)"
      :off="!enabled"
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
    </LogRetention>

    <p v-if="!dnsOn" class="text-sm text-ink-muted">The DNS server is off.</p>
    <p v-else-if="unreadable" role="alert" class="text-sm text-bad">
      {{ log.error.value }}
    </p>
    <SectionCard v-else ref="card" title="Queries" flush>
      <template #intro>
        <template v-if="log.held.value">
          {{ heldLine(log.held.value, log.oldest.value, inFiles) }}
          <template v-if="summary?.total">
            {{ formatCount(summary.blocked) }} blocked ({{ blockedPct }}%) ·
            {{ formatCount(summary.clients) }} clients
            <span v-if="summary.dropped"> · {{ formatCount(summary.dropped) }} dropped</span>
          </template>
          <template v-if="inFiles">Older ones are read from the files.</template>
        </template>
      </template>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(log.streamError.value)" />
        <ClearLogButton
          v-if="running"
          name="query log"
          :description="
            inFiles
              ? 'Every answer it holds, and the counts per list, are dropped, and its files are deleted.'
              : 'Every answer it holds, and the counts per list, are dropped.'
          "
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <div class="flex flex-wrap items-center gap-3">
          <select v-model="filter.status" class="input w-40 max-sm:w-full" aria-label="Status">
            <option value="">Any status</option>
            <option value="blocked">Blocked</option>
            <option value="ok">Answered</option>
            <option value="nxdomain">No such name</option>
            <option value="nodata">No data</option>
            <option value="servfail">Failed</option>
          </select>
          <select v-model="filter.list" class="input w-40 max-sm:w-full" aria-label="List">
            <option value="">Any list</option>
            <option v-for="l in lists" :key="l.name" :value="l.name">{{ l.name }}</option>
          </select>
          <select v-model="filter.type" class="input w-32 max-sm:w-full" aria-label="Type">
            <option value="">Any type</option>
            <option v-for="t in ['A', 'AAAA', 'HTTPS', 'PTR', 'SRV', 'TXT', 'MX']" :key="t">
              {{ t }}
            </option>
          </select>
          <SearchBox v-model="query" placeholder="name, client, device, or list" />
        </div>
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      </div>

      <p v-if="log.page.value && !running" class="card-strip border-t border-line text-ink-muted">
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
              <td colspan="7" class="text-ink-muted">{{ empty }}</td>
            </tr>
            <template v-for="e in rows" :key="e.seq">
              <tr class="max-sm:after:order-4 max-sm:after:basis-full max-sm:after:content-['']">
                <td class="when max-sm:order-1">
                  {{ formatWhen(e.time) }}
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
                    class="link-action"
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

      <LoadMore
        v-if="running"
        :more="log.more.value"
        :busy="log.readingMore.value"
        :rows="rows.length"
        :searched-to="log.searchedTo.value"
        @load="log.loadMore()"
      />
    </SectionCard>
  </div>
</template>
