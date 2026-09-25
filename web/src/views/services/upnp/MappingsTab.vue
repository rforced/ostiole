<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { byAddress, byNumber, byText, useSort } from '@/lib/sort'

const mappings = ref([])

const load = useAsync(async () => {
  mappings.value = await api.upnp.mappings()
})
onMounted(load.run)

/** Ports read upwards, as a port list does. */
const sort = useSort(mappings, {
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
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
      </template>
      <div v-if="load.error.value" class="card-strip">
        <p role="alert" class="text-bad">{{ load.error.value }}</p>
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
          <tr v-if="!mappings.length">
            <td colspan="4" class="text-ink-muted">
              {{ load.updatedAt.value ? 'No mappings.' : 'Reading…' }}
            </td>
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
