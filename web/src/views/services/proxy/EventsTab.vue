<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { formatCount, formatDuration, formatWhen } from '@/lib/format'
import { heldLine, useLog } from '@/lib/log'
import { VERDICTS, eventValues, openSeconds, ruleLines } from '@/lib/proxyEvents'
import { useProxyStatus } from '@/lib/proxyStatus'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ExclusionDialog from '@/views/services/proxy/ExclusionDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const { stage } = useProxyStatus()

/** The retention, kept out of the model while every field is the default. */
const retention = computed({
  get: () => config.proxy.events ?? {},
  set: (v) => config.setProxy({ events: Object.keys(v).length ? v : undefined }),
})

const verdict = ref('')
const card = ref(null)

const log = useLog({
  read: (params, signal) => api.proxy.events(params, signal),
  stream: '/api/v1/proxy/events/stream',
  filters: () => (verdict.value ? { verdict: verdict.value } : {}),
  keep: (e) => !verdict.value || e.verdict === verdict.value,
  values: eventValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the router's log, its files included, and reads it again. */
const clear = useAsync(async () => {
  await api.proxy.clearEvents()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

/** What the proxy is doing means no new events, whatever the log holds. */
const idle = computed(() => stage.value === 'off' || stage.value === 'unapplied')

const empty = computed(() => {
  const q = query.value.trim()
  if (q) return emptyText(log, `Nothing matches "${q}".`)
  return emptyText(log, verdict.value ? 'Nothing matches this view.' : 'No events.')
})

/** The WAF profile a site is inspected by, which an exclusion goes into. */
function profileOf(siteID) {
  return (config.proxy.sites ?? []).find((s) => s.id === siteID)?.waf ?? ''
}

/** The exclusion the dialog starts from, and the profile it goes into. */
const excluding = ref(null)
const excludeOpen = ref(false)

/**
 * The whole rule goes straight into the draft. On a path it goes through
 * the dialog first, where the path can be cut down to the part that
 * repeats: /Items rather than one item's path.
 */
function exclude(event, rule, onPath) {
  const profile = profileOf(event.site)
  if (!profile) return
  const exclusion = { rule: String(rule.id) }
  if (onPath) exclusion.path = pathOf(event.uri)
  if (rule.message) exclusion.description = rule.message
  if (!onPath) {
    config.addExclusion(profile, exclusion)
    return
  }
  excluding.value = { profile, exclusion }
  excludeOpen.value = true
}

/** The path a request asked for, without its query. */
function pathOf(uri) {
  return String(uri ?? '/').split('?')[0] || '/'
}

function hasQuery(uri) {
  return String(uri ?? '').includes('?')
}

/**
 * An address as it may wrap: past 20 characters, which only IPv6 goes, in
 * two halves at a colon, each kept whole.
 */
function halves(addr) {
  const s = String(addr ?? '')
  const colons = [...s.matchAll(/:/g)].map((m) => m.index)
  if (s.length <= 20 || !colons.length) return [s]
  const near = (i) => Math.abs(i + 1 - s.length / 2)
  const mid = colons.reduce((a, b) => (near(b) < near(a) ? b : a)) + 1
  return [s.slice(0, mid), s.slice(mid)]
}
</script>

<template>
  <div class="space-y-5">
    <LogRetention v-model="retention" log="events" title="WAF events" />

    <SectionCard ref="card" title="Events" flush>
      <template #intro>
        {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value, inFiles) : '' }}
        What the WAF matched.
        <template v-if="inFiles">Older ones are read from the files.</template>
        <template v-else>Kept in memory, so a restart empties it.</template>
      </template>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(error)" />
        <ClearLogButton
          name="WAF events"
          noun="event"
          journal
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <div class="flex flex-wrap items-center gap-3">
          <select v-model="verdict" class="input w-40 max-sm:w-full" aria-label="Verdict">
            <option value="">All</option>
            <option value="blocked">Blocked</option>
            <option value="would-block">Would block</option>
            <option value="matched">Matched</option>
          </select>
          <SearchBox v-model="query" placeholder="site, client, request, verdict, or rule" />
        </div>
        <p v-if="idle" class="text-ink-muted">The proxy is off, so no new events arrive.</p>
        <ErrorLine v-if="error">{{ error }}</ErrorLine>
      </div>
      <!-- Below 1280 px an event is a block of lines: when, the verdict and
           the site; who asked for what; the rules it matched. -->
      <table class="table table-flow-xl">
        <thead>
          <tr>
            <th>Time</th>
            <th>Site</th>
            <th>Client</th>
            <th>Request</th>
            <th>Verdict</th>
            <th>Rules</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="6" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr
            v-for="e in rows"
            :key="e.seq"
            class="max-xl:after:order-4 max-xl:after:basis-full max-xl:after:content-['']"
          >
            <td class="when max-xl:order-1">
              {{ formatWhen(e.logged) }}
              <div v-if="openSeconds(e)" :title="`Opened ${formatWhen(e.time)}`">
                open {{ formatDuration(openSeconds(e)) }}
              </div>
            </td>
            <td class="font-mono text-code max-xl:order-3 xl:whitespace-nowrap">
              {{ e.site || '—' }}
            </td>
            <td class="font-mono text-code max-xl:order-5">
              <template v-for="(half, i) in halves(e.client)" :key="i"
                ><wbr v-if="i" /><span class="whitespace-nowrap">{{ half }}</span></template
              >
            </td>
            <td
              class="font-mono text-code max-xl:order-6 max-xl:min-w-40 max-xl:flex-1 xl:w-1/3 xl:min-w-32 xl:max-w-0"
            >
              <details>
                <summary class="block cursor-pointer truncate" :title="`${e.method} ${e.uri}`">
                  {{ e.method }} {{ pathOf(e.uri)
                  }}<span v-if="hasQuery(e.uri)" class="text-ink-muted">?…</span>
                </summary>
                <div class="mt-1 break-all text-ink-2">{{ e.uri }}</div>
              </details>
            </td>
            <td class="whitespace-nowrap max-xl:order-2">
              <span class="badge" :class="VERDICTS[e.verdict]?.tone">
                {{ VERDICTS[e.verdict]?.label ?? e.verdict }}
              </span>
              <span v-if="e.status" class="text-ink-muted tabular-nums"> · {{ e.status }}</span>
            </td>
            <!-- The request and the rules share the width left and cut their
                 text to it. Narrower than their minimums, the table scrolls
                 in its card. -->
            <td class="max-xl:order-7 max-xl:basis-full xl:w-2/3 xl:min-w-60 xl:max-w-0">
              <div
                v-for="r in ruleLines(e.rules)"
                :key="r.id"
                class="flex flex-wrap items-center gap-x-2 gap-y-1"
              >
                <span class="badge shrink-0 font-mono" :title="r.data || r.message">{{
                  r.id
                }}</span>
                <span
                  v-if="r.count > 1"
                  class="shrink-0 text-xs text-ink-muted tabular-nums"
                  :title="`Matched ${r.count} times`"
                  >×{{ r.count }}</span
                >
                <span class="min-w-32 flex-1 text-ink-muted xl:truncate" :title="r.message">{{
                  r.message
                }}</span>
                <!-- The links keep together, under the message when the
                     column is narrow. -->
                <span v-if="!profileOf(e.site)" class="ml-auto shrink-0 text-ink-muted">
                  The site has no WAF profile.
                </span>
                <span v-else-if="!auth.readOnly" class="ml-auto flex shrink-0 gap-2">
                  <button
                    type="button"
                    class="link-action whitespace-nowrap"
                    @click="exclude(e, r, false)"
                  >
                    Exclude
                  </button>
                  <button
                    type="button"
                    class="link-action whitespace-nowrap"
                    @click="exclude(e, r, true)"
                  >
                    Exclude on this path
                  </button>
                </span>
              </div>
              <p v-if="e.moreMatches" class="text-ink-muted">
                {{ formatCount(e.moreMatches) }} more matches not kept.
              </p>
            </td>
          </tr>
        </tbody>
      </table>
      <LoadMore
        :more="log.more.value"
        :busy="log.readingMore.value"
        :rows="rows.length"
        :searched-to="log.searchedTo.value"
        @load="log.loadMore()"
      />
    </SectionCard>

    <ExclusionDialog
      v-if="excluding"
      v-model:open="excludeOpen"
      :profile="excluding.profile"
      :exclusion="excluding.exclusion"
    />
  </div>
</template>
