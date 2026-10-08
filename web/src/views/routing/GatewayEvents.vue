<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { eventFor, eventText, eventValues } from '@/lib/gateways'
import { heldLine, useLog } from '@/lib/log'
import { useConfigStore } from '@/stores/config'

/** What changed on the gateways, newest first. */
const config = useConfigStore()

const card = ref(null)
const log = useLog({
  read: (params, signal) => api.gatewayHistory.events(params, signal),
  stream: '/api/v1/gateways/events/stream',
  values: eventValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the router's events, their files included, and reads them again. */
const clear = useAsync(async () => {
  await api.gatewayHistory.clearEvents()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)
/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

const empty = computed(() => {
  const q = query.value.trim()
  return emptyText(log, q ? `Nothing matches "${q}".` : 'No events yet.')
})
</script>

<template>
  <SectionCard ref="card" title="Gateway events" flush>
    <template #intro>
      {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value) : '' }}
      Kept 31 days.
      <template v-if="inFiles">The files keep them through a restart.</template>
      <template v-else>Kept in memory, so a restart empties it.</template>
    </template>
    <template #actions>
      <LiveButton v-model="live" :failing="Boolean(error)" />
      <ClearLogButton
        name="gateway events"
        noun="event"
        :busy="clear.busy.value"
        @confirm="clear.run()"
      />
    </template>
    <div class="card-strip space-y-3">
      <SearchBox v-model="query" placeholder="gateway, family, what happened, or error" />
      <ErrorLine v-if="error">{{ error }}</ErrorLine>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Time</th>
          <th>Gateway</th>
          <th>What</th>
          <th class="num">For</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!rows.length">
          <td colspan="4" class="text-ink-muted">{{ empty }}</td>
        </tr>
        <tr v-for="e in rows" :key="e.seq">
          <td data-label="Time" class="when">
            {{ formatWhen(e.time) }}
          </td>
          <td data-label="Gateway" class="font-mono text-code">{{ e.gateway }}</td>
          <td data-label="What">
            {{ eventText(e) }}
            <div v-if="e.error" class="text-xs text-ink-muted">{{ e.error }}</div>
          </td>
          <td data-label="For" class="num">{{ eventFor(e) || '—' }}</td>
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
</template>
