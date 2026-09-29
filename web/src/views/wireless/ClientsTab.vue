<script setup>
import { computed, ref } from 'vue'

import RandomMacBadge from '@/components/RandomMacBadge.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatDuration } from '@/lib/format'
import { byAddress, byNumber, byText, useSort } from '@/lib/sort'
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

const COLUMNS = [
  ['client', 'Client'],
  ['address', 'Address'],
  ['network', 'Network'],
  ['signal', 'Signal'],
  ['connected', 'Connected'],
  ['traffic', 'Traffic'],
]

/** Connected runs newest first, as a time would. */
const sort = useSort(clients, {
  client: byText((c) => c.hostname || c.mac),
  address: byAddress((c) => c.address),
  network: byText(ssidOf),
  signal: byNumber((c) => c.signalDbm),
  connected: byNumber((c) => c.connectedSeconds, 'asc'),
  traffic: byNumber((c) => (c.rxBytes ?? 0) + (c.txBytes ?? 0)),
})
const rows = sort.sorted
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Clients" :count="clients.length" flush>
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
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
            <SortHeader by="client" :sort="sort">Client</SortHeader>
            <SortHeader by="address" :sort="sort">Address</SortHeader>
            <SortHeader by="network" :sort="sort">Network</SortHeader>
            <SortHeader by="signal" :sort="sort">Signal</SortHeader>
            <th>Rates</th>
            <SortHeader by="connected" :sort="sort">Connected</SortHeader>
            <SortHeader by="traffic" :sort="sort">Traffic</SortHeader>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!clients.length">
            <td colspan="7" class="text-ink-muted">
              {{ nothingRunning ? 'No radio is running.' : 'No clients.' }}
            </td>
          </tr>
          <tr v-for="c in rows" :key="c.mac">
            <td data-label="">
              <div class="font-medium">
                {{ c.hostname || c.mac }}
                <RandomMacBadge v-if="!c.hostname" :mac="c.mac" />
              </div>
              <div v-if="c.hostname" class="text-xs text-ink-muted">
                {{ c.mac }}
                <RandomMacBadge :mac="c.mac" />
              </div>
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
