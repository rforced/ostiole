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
import { discoveryActive, discoveryValues } from '@/lib/discovery'
import { formatWhen } from '@/lib/format'
import { heldLine, useLog } from '@/lib/log'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()

/** The retention, kept out of the model while every field is the default. */
const retention = computed({
  get: () => config.ensureDiscovery().log ?? {},
  set: (v) => config.setDiscovery({ log: Object.keys(v).length ? v : null }),
})

const card = ref(null)
const log = useLog({
  read: (params, signal) => api.services.discoveryLog(params, signal),
  stream: '/api/v1/discovery/log/stream',
  values: discoveryValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the router's log, its files included, and reads it again. */
const clear = useAsync(async () => {
  await api.services.clearDiscoveryLog()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))
const off = computed(() => !discoveryActive(config.saved))

const empty = computed(() => {
  const q = query.value.trim()
  return emptyText(log, q ? `Nothing matches "${q}".` : 'No packets.')
})
</script>

<template>
  <div class="space-y-5">
    <LogRetention
      v-model="retention"
      log="discovery"
      title="Discovery log"
      intro="Each packet the relay sees, sent on or dropped."
    />

    <SectionCard ref="card" title="Log" flush>
      <template #intro>
        {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value, inFiles) : '' }}
        <template v-if="inFiles">Older ones are read from the files.</template>
        <template v-else>Kept in memory, so a restart empties it.</template>
      </template>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(error)" />
        <ClearLogButton
          name="Discovery log"
          noun="packet"
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <SearchBox v-model="query" placeholder="name, interface, address, or reason" />
        <p v-if="off" class="text-ink-muted">Discovery is off, so no new packets arrive.</p>
        <ErrorLine v-if="error">{{ error }}</ErrorLine>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Time</th>
            <th>Protocol</th>
            <th>Kind</th>
            <th>From</th>
            <th>To</th>
            <th>Name</th>
            <th>Source</th>
            <th>Dropped</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="8" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr v-for="e in rows" :key="e.seq" v-memo="[e]">
            <td class="when" data-label="Time">{{ formatWhen(e.time) }}</td>
            <td data-label="Protocol">{{ e.protocol === 'ssdp' ? 'SSDP' : 'mDNS' }}</td>
            <td data-label="Kind">
              <span class="badge">{{ e.kind }}</span>
            </td>
            <td class="font-mono text-code" data-label="From">{{ e.from }}</td>
            <td class="font-mono text-code" data-label="To">{{ (e.to ?? []).join(', ') }}</td>
            <td class="font-mono text-code break-all" data-label="Name">{{ e.name }}</td>
            <td class="font-mono text-code" data-label="Source">{{ e.source }}</td>
            <td class="text-ink-muted" data-label="Dropped">{{ e.dropped }}</td>
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
