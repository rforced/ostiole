<script setup>
import { computed, ref, watch } from 'vue'

import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { useSearch } from '@/lib/search'
import { byText, byTime, useSort } from '@/lib/sort'

/** How often Live reads the announcements again. */
const LIVE_MS = 5000

const list = ref([])
const live = ref(true)

const load = useAsync(
  async () => {
    list.value = (await api.services.discoveryAnnouncements())?.announcements ?? []
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

const { query, shown } = useSearch(list, (a) => ({
  values: [a.name, a.type, a.host, ...(a.addresses ?? [])],
}))

const sort = useSort(
  shown,
  {
    type: byText((a) => a.type),
    name: byText((a) => a.name),
    lastSeen: byTime((a) => a.lastSeen),
  },
  { by: 'lastSeen', tie: 'name' },
)
const rows = sort.sorted

const empty = computed(() =>
  emptyText(
    load,
    list.value.length ? `Nothing matches "${query.value.trim()}".` : 'No announcements.',
  ),
)
</script>

<template>
  <SectionCard title="Announcements" :count="list.length" flush>
    <template #actions>
      <LiveButton v-model="live" :failing="Boolean(load.error.value)" />
    </template>
    <div class="card-strip-row">
      <SearchBox
        v-model="query"
        placeholder="name, type, host, or address"
        :shown="shown.length"
        :total="list.length"
      />
    </div>
    <div v-if="load.error.value" class="card-strip">
      <ErrorLine>{{ load.error.value }}</ErrorLine>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Interface</th>
          <SortHeader by="type" :sort="sort">Type</SortHeader>
          <SortHeader by="name" :sort="sort">Name</SortHeader>
          <th>Host and port</th>
          <th>Addresses</th>
          <SortHeader by="lastSeen" :sort="sort">Last seen</SortHeader>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!rows.length">
          <td colspan="6" class="text-ink-muted">{{ empty }}</td>
        </tr>
        <tr v-for="a in rows" :key="`${a.protocol} ${a.interface} ${a.name}`">
          <td class="font-mono text-code" data-label="Interface">{{ a.interface }}</td>
          <td class="font-mono text-code break-all" data-label="Type">{{ a.type }}</td>
          <td class="break-all" data-label="Name">{{ a.name }}</td>
          <td data-label="Host and port">
            <div class="font-mono text-code break-all">{{ a.host || '—' }}</div>
            <div v-if="a.port" class="font-mono text-xs text-ink-muted">{{ a.port }}</div>
          </td>
          <td class="font-mono text-code" data-label="Addresses">
            <div v-for="addr in a.addresses ?? []" :key="addr">{{ addr }}</div>
          </td>
          <td class="when" data-label="Last seen">{{ formatWhen(a.lastSeen) }}</td>
        </tr>
      </tbody>
    </table>
  </SectionCard>
</template>
