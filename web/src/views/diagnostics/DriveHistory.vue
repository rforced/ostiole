<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { readingValues } from '@/lib/driveHistory'
import { formatCount } from '@/lib/format'
import { heldLine, useLog } from '@/lib/log'
import { useConfigStore } from '@/stores/config'

/** What the hourly check read on each drive, newest first. */
const config = useConfigStore()
const card = ref(null)
const log = useLog({
  read: (params, signal) => api.diagnostics.driveHistory(params, signal),
  stream: '/api/v1/diagnostics/drives/history/stream',
  values: readingValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the router's log, its files included, and reads it again. */
const clear = useAsync(async () => {
  await api.diagnostics.clearDriveHistory()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)
/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

const empty = computed(() => {
  if (!log.updatedAt.value) return log.reading.value ? 'Reading…' : 'No readings yet.'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return 'No readings yet.'
})

const orDash = (v, unit = '') => (v == null ? '—' : `${formatCount(v)}${unit}`)
</script>

<template>
  <SectionCard ref="card" title="History" flush>
    <template #intro>
      {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value, inFiles) : '' }}
      Each drive, once an hour.
      <template v-if="inFiles">Older ones are read from the files.</template>
      <template v-else>A week is kept in memory, so a restart empties it.</template>
    </template>
    <template #actions>
      <LiveButton v-model="live" :failing="Boolean(error)" />
      <ClearLogButton
        name="drive history"
        noun="reading"
        :busy="clear.busy.value"
        @confirm="clear.run()"
      />
    </template>
    <div class="card-strip space-y-2">
      <SearchBox v-model="query" placeholder="drive, model, serial, health, or temperature" />
      <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Time</th>
          <th>Drive</th>
          <th>Health</th>
          <th>Temperature</th>
          <th>Wear</th>
          <th>Bad sectors</th>
          <th>Powered on</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!rows.length">
          <td colspan="7" class="text-ink-muted">{{ empty }}</td>
        </tr>
        <tr v-for="r in rows" :key="r.seq">
          <td data-label="Time" class="text-xs whitespace-nowrap text-ink-muted tabular-nums">
            {{ new Date(r.time).toLocaleString() }}
          </td>
          <td data-label="Drive">
            <span class="font-mono text-code">{{ r.drive }}</span>
            <div v-if="r.model" class="text-xs text-ink-muted">{{ r.model }}</div>
          </td>
          <td data-label="Health">
            <span class="badge" :class="{ 'badge-bad': r.health === 'failed' }">{{
              r.health
            }}</span>
          </td>
          <td data-label="Temperature" class="tabular-nums">{{ orDash(r.temperature, ' °C') }}</td>
          <td data-label="Wear" class="tabular-nums">{{ orDash(r.wear, '%') }}</td>
          <td data-label="Bad sectors" class="tabular-nums">{{ orDash(r.bad) }}</td>
          <td data-label="Powered on" class="tabular-nums">{{ orDash(r.powerOnHours, ' h') }}</td>
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
