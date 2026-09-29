<script setup>
import { computed, onMounted, ref, watch } from 'vue'

import LiveButton from '@/components/LiveButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useSearch } from '@/lib/search'
import { byAddress, byNumber, byText, useSort } from '@/lib/sort'

/** How often Live reads the mappings again. */
const LIVE_MS = 2000

const mappings = ref([])
const live = ref(false)

// Live polls; otherwise the tab reads on opening and on Refresh.
const load = useAsync(
  async () => {
    mappings.value = await api.upnp.mappings()
  },
  { interval: LIVE_MS, autostart: false },
)
onMounted(load.run)
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

const empty = computed(() => {
  if (!load.updatedAt.value) return 'Reading…'
  if (!mappings.value.length) return 'No mappings.'
  return `Nothing matches "${query.value.trim()}".`
})

/** Ports read upwards, as a port list does. */
const sort = useSort(shown, {
  protocol: byText((m) => m.protocol),
  external: byNumber((m) => m.externalPort, 'asc'),
  client: byAddress((m) => m.internal),
  internal: byNumber((m) => m.internalPort, 'asc'),
})
const rows = sort.sorted
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
        <LiveButton v-model="live" :failing="Boolean(load.error.value)" />
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
      </template>
      <div class="card-strip flex flex-wrap items-center gap-3">
        <SearchBox
          v-model="query"
          placeholder="client, port, or protocol"
          :shown="shown.length"
          :total="mappings.length"
        />
        <p v-if="load.error.value" role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
      <table class="table">
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
            <td>
              <span class="badge">{{ m.protocol }}</span>
            </td>
            <td class="font-mono text-code">{{ m.externalPort }}</td>
            <td class="font-mono text-code">{{ m.internal }}</td>
            <td class="font-mono text-code">{{ m.internalPort }}</td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
