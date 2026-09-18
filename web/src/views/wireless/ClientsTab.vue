<script setup>
import { computed, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
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
  <div class="space-y-3">
    <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run()" />
    <p v-if="load.error.value" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ load.error.value }}
    </p>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
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
            <td colspan="7" class="text-neutral-500">
              {{ nothingRunning ? 'Nothing to show.' : 'No clients.' }}
            </td>
          </tr>
          <tr v-for="c in clients" :key="c.mac">
            <td>
              <div class="font-medium">{{ c.hostname || c.mac }}</div>
              <div v-if="c.hostname" class="text-xs text-neutral-500">{{ c.mac }}</div>
            </td>
            <td class="font-mono text-code">{{ c.address || '—' }}</td>
            <td>
              {{ ssidOf(c) }}
              <span class="text-xs text-neutral-500">{{ c.radio }}</span>
            </td>
            <td class="font-mono text-code">{{ c.signalDbm }} dBm</td>
            <td class="text-code">
              <div>↓ {{ c.rxBitrate || '—' }}</div>
              <div>↑ {{ c.txBitrate || '—' }}</div>
            </td>
            <td>{{ formatDuration(c.connectedSeconds) }}</td>
            <td class="text-code">
              <div>↓ {{ formatBytes(c.rxBytes) }}</div>
              <div>↑ {{ formatBytes(c.txBytes) }}</div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
