<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'

const leases = ref([])

const load = useAsync(async () => {
  leases.value = await api.services.leases()
})
onMounted(load.run)
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Leases" :count="leases.length" flush>
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
            <th>Address</th>
            <th>Client</th>
            <th>Hostname</th>
            <th>Expires</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!leases.length" key="empty" class="row-static">
            <td colspan="4" class="text-ink-muted">
              {{ load.updatedAt.value ? 'No leases.' : 'Reading…' }}
            </td>
          </tr>
          <tr v-for="l in leases" :key="l.ip + (l.mac || l.clientId)">
            <td class="font-mono text-code">
              {{ l.ip }}
              <span v-if="l.family === 6" class="badge ml-1">v6</span>
            </td>
            <!-- DHCPv6 identifies clients by DUID, so there is no MAC. -->
            <td class="font-mono text-code">{{ l.mac || l.clientId || '—' }}</td>
            <td class="font-mono text-code">{{ l.hostname }}</td>
            <td>
              {{ l.static ? 'static' : new Date(l.expires).toLocaleString() }}
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>
  </div>
</template>
