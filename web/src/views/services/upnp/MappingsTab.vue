<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'

const mappings = ref([])

const load = useAsync(async () => {
  mappings.value = await api.upnp.mappings()
})
onMounted(load.run)
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
            <th>Protocol</th>
            <th>External port</th>
            <th>Client</th>
            <th>Internal port</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!mappings.length" key="empty" class="row-static">
            <td colspan="4" class="text-ink-muted">
              {{ load.updatedAt.value ? 'No mappings.' : 'Reading…' }}
            </td>
          </tr>
          <tr
            v-for="m in mappings"
            :key="`${m.protocol}:${m.externalPort}:${m.internal}:${m.internalPort}`"
          >
            <td>
              <span class="badge">{{ m.protocol }}</span>
            </td>
            <td class="font-mono text-code">{{ m.externalPort }}</td>
            <td class="font-mono text-code">{{ m.internal }}</td>
            <td class="font-mono text-code">{{ m.internalPort }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>
  </div>
</template>
