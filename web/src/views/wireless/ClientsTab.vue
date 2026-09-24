<script setup>
import { computed, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatDuration } from '@/lib/format'
import { useConfigStore } from '@/stores/config'

/** How often the stations are read while the tab is shown. */
const POLL_MS = 5000

const config = useConfigStore()
const clients = ref([])

const load = useAsync(
  async () => {
    clients.value = await api.wireless.clients()
  },
  { interval: POLL_MS, immediate: true },
)

/** Nothing is meant to be transmitting, so nothing is meant to be here. */
const nothingRunning = computed(
  () => !config.wirelessNetworks.some((i) => i.enabled) || !config.radios.some((r) => r.enabled),
)

function ssidOf(c) {
  return c.ssid || c.interface
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Clients" :count="clients.length" flush>
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run()"
        />
      </template>
      <div v-if="load.error.value" class="card-strip">
        <p role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Client</th>
            <th>Address</th>
            <th>Network</th>
            <th>Signal</th>
            <th>Rates</th>
            <th>Connected</th>
            <th>Traffic</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!clients.length">
            <td colspan="7" class="text-ink-muted">
              {{ nothingRunning ? 'No radio is running.' : 'No clients.' }}
            </td>
          </tr>
          <tr v-for="c in clients" :key="c.mac">
            <td data-label="">
              <div class="font-medium">{{ c.hostname || c.mac }}</div>
              <div v-if="c.hostname" class="text-xs text-ink-muted">{{ c.mac }}</div>
            </td>
            <td class="font-mono text-code" data-label="Address">{{ c.address || '—' }}</td>
            <td data-label="Network">
              {{ ssidOf(c) }}
              <span class="text-xs text-ink-muted">{{ c.radio }}</span>
            </td>
            <td class="font-mono text-code" data-label="Signal">{{ c.signalDbm }} dBm</td>
            <td class="text-code" data-label="Rates">
              <div>↓ {{ c.rxBitrate || '—' }}</div>
              <div>↑ {{ c.txBitrate || '—' }}</div>
            </td>
            <td data-label="Connected">{{ formatDuration(c.connectedSeconds) }}</td>
            <td class="text-code" data-label="Traffic">
              <div>↓ {{ formatBytes(c.rxBytes) }}</div>
              <div>↑ {{ formatBytes(c.txBytes) }}</div>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
