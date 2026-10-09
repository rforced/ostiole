<script setup>
import { computed, ref, watch } from 'vue'

import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { useSearch } from '@/lib/search'
import { byAddress, byNumber, byText, useSort } from '@/lib/sort'

/** How often Live reads the mappings again. */
const LIVE_MS = 2000

const mappings = ref([])
const live = ref(true)

const load = useAsync(
  async () => {
    mappings.value = await api.upnp.mappings()
  },
  // Live owns the poll, and starts on.
  { interval: LIVE_MS, immediate: true },
)
watch(live, (on) => {
  if (on) {
    load.run()
    load.start()
  } else {
    load.stop()
  }
})

const { query, shown } = useSearch(mappings, (m) => ({
  values: [m.protocol, m.externalPort, m.internal, m.internalPort],
}))

const empty = computed(() =>
  emptyText(
    load,
    mappings.value.length ? `Nothing matches "${query.value.trim()}".` : 'No mappings.',
  ),
)

/** Ports read upwards, as a port list does. */
const sort = useSort(shown, {
  protocol: byText((m) => m.protocol),
  external: byNumber((m) => m.externalPort, 'asc'),
  client: byAddress((m) => m.internal),
  internal: byNumber((m) => m.internalPort, 'asc'),
})
const rows = sort.sorted
const COLUMNS = [
  ['protocol', 'Protocol'],
  ['external', 'External port'],
  ['client', 'Client'],
  ['internal', 'Internal port'],
]
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Mappings"
      :count="mappings.length"
      intro="Clients re-open these after an apply, so the list is empty for a moment."
      flush
    >
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <LiveButton v-model="live" :failing="Boolean(load.error.value)" />
      </template>
      <div class="card-strip-row">
        <SearchBox
          v-model="query"
          placeholder="client, port, or protocol"
          :shown="shown.length"
          :total="mappings.length"
        />
        <ErrorLine v-if="load.error.value">{{ load.error.value }}</ErrorLine>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <SortHeader by="protocol" :sort="sort">Protocol</SortHeader>
            <SortHeader by="external" :sort="sort">External port</SortHeader>
            <SortHeader by="client" :sort="sort">Client</SortHeader>
            <SortHeader by="internal" :sort="sort">Internal port</SortHeader>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="4" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr
            v-for="m in rows"
            :key="`${m.protocol}:${m.externalPort}:${m.internal}:${m.internalPort}`"
          >
            <td data-label="Protocol">
              <span class="badge">{{ m.protocol }}</span>
            </td>
            <td class="font-mono text-code" data-label="External port">{{ m.externalPort }}</td>
            <td class="font-mono text-code" data-label="Client">{{ m.internal }}</td>
            <td class="font-mono text-code" data-label="Internal port">{{ m.internalPort }}</td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
