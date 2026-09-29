<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes } from '@/lib/format'
import { heldLine, useLog } from '@/lib/log'
import { records } from '@/lib/logs'
import { requestValues, tookText } from '@/lib/proxyRequests'
import { useProxyStatus } from '@/lib/proxyStatus'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const { stage } = useProxyStatus()

/** The retention, kept out of the model while every field is the default. */
const retention = computed({
  get: () => config.proxy.requests ?? {},
  set: (v) => config.setProxy({ requests: Object.keys(v).length ? v : undefined }),
})

const card = ref(null)
const log = useLog({
  read: (params, signal) => api.proxy.requests(params, signal),
  stream: '/api/v1/proxy/requests/stream',
  values: requestValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the router's log, its files included, and reads it again. */
const clear = useAsync(async () => {
  await api.proxy.clearRequests()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))
/** The level the router runs at keeps a line per request. */
const kept = computed(() => records(config.saved))

/** What the proxy is doing means no new requests, whatever the log holds. */
const idle = computed(() => stage.value === 'off' || stage.value === 'unapplied')

const empty = computed(() => {
  if (!log.updatedAt.value) return log.reading.value ? 'Reading…' : 'No requests.'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return 'No requests.'
})
</script>

<template>
  <div class="space-y-5">
    <LogRetention
      v-model="retention"
      log="requests"
      title="Requests"
      intro="A line for each request the proxy answers, without its query string or headers."
    />

    <SectionCard ref="card" title="Requests" flush>
      <template #intro>
        {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value, inFiles) : '' }}
        <template v-if="inFiles">Older ones are read from the files.</template>
        <template v-else>Kept in memory, so a restart empties it.</template>
      </template>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(error)" />
        <ClearLogButton
          v-if="kept"
          name="proxy requests"
          noun="request"
          journal
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <SearchBox v-model="query" placeholder="site, client, request, status, or user agent" />
        <p v-if="!kept" class="text-ink-muted">
          Requests are kept at the Info and Debug log levels, under System, General.
        </p>
        <p v-else-if="idle" class="text-ink-muted">The proxy is off, so no new requests arrive.</p>
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      </div>
      <table class="table table-flow-xl">
        <thead>
          <tr>
            <th>Time</th>
            <th>Site</th>
            <th>Client</th>
            <th>Request</th>
            <th>Status</th>
            <th>User agent</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="6" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr v-for="r in rows" :key="r.seq">
            <td class="text-xs whitespace-nowrap text-ink-muted tabular-nums">
              {{ new Date(r.time).toLocaleString() }}
            </td>
            <td class="font-mono text-code xl:whitespace-nowrap">{{ r.site || '—' }}</td>
            <td class="font-mono text-code">{{ r.client }}</td>
            <td class="font-mono text-code xl:w-1/2 xl:max-w-0">
              <div class="truncate" :title="`${r.method} ${r.host}${r.path}`">
                {{ r.method }} {{ r.host }}{{ r.path }}
              </div>
            </td>
            <td class="whitespace-nowrap tabular-nums">
              {{ r.status }}
              <div class="text-xs text-ink-muted">
                {{ formatBytes(r.bytes) }} · {{ tookText(r.duration) }}
              </div>
            </td>
            <td class="text-ink-muted xl:w-1/4 xl:max-w-0">
              <div class="truncate" :title="r.agent">{{ r.agent || '—' }}</div>
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
  </div>
</template>
