<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { byAddress, byText, byTime, useSort } from '@/lib/sort'

const status = ref(null)

const load = useAsync(async () => {
  status.value = await api.tailscale.status()
})
onMounted(load.run)

/** A name with the tailnet suffix on it reads better without the dot. */
function name(peer) {
  return (peer.dnsName || peer.hostName).replace(/\.$/, '')
}

/** A time is worth showing for a peer that is not there to speak for itself. */
function seen(peer) {
  if (peer.online || !peer.lastSeen) return '—'
  return new Date(peer.lastSeen).toLocaleString()
}

/**
 * How this peer is reached, in the words `tailscale status` uses. The
 * region is a peer's DERP home whether or not anything is relayed through
 * it, and the direct endpoint is cleared when the connection goes idle, so
 * neither says anything until traffic is flowing.
 */
function path(peer) {
  if (!peer.active) return 'idle'
  return peer.directAddr ? `direct ${peer.directAddr}` : `relay ${peer.relay}`
}

const COLUMNS = [
  ['node', 'Node'],
  ['addresses', 'Addresses'],
  ['os', 'OS'],
  ['seen', 'Last seen'],
]

/** A peer online now was last seen now. */
const sort = useSort(() => (status.value?.running ? status.value.peers : []), {
  node: byText(name),
  addresses: byAddress((p) => p.ips?.[0]),
  os: byText((p) => p.os),
  seen: byTime((p) => (p.online ? new Date() : p.lastSeen)),
})
const peers = sort.sorted
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Peers" :count="status?.running ? status.peers.length : 0" flush>
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
      </template>
      <div v-if="load.error.value" class="card-strip">
        <p role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <SortHeader by="node" :sort="sort">Node</SortHeader>
            <SortHeader by="addresses" :sort="sort">Addresses</SortHeader>
            <SortHeader by="os" :sort="sort">OS</SortHeader>
            <SortHeader by="seen" :sort="sort">Last seen</SortHeader>
            <th>Path</th>
            <th>Routes</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!status?.running">
            <td colspan="6" class="text-ink-muted">
              {{ load.updatedAt.value ? 'Tailscale is not running.' : 'Reading…' }}
            </td>
          </tr>
          <tr v-else-if="!status.peers.length">
            <td colspan="6" class="text-ink-muted">No peers.</td>
          </tr>
          <tr v-for="p in peers" :key="p.dnsName || p.hostName">
            <td data-label="">
              <div class="font-mono">{{ name(p) }}</div>
              <span v-if="p.online" class="badge badge-ok">online</span>
              <span v-else class="badge">offline</span>
              <span v-if="p.exitNode" class="badge ml-1">exit node</span>
              <span v-if="p.expired" class="badge badge-warn ml-1">expired</span>
            </td>
            <td class="font-mono text-code" data-label="Addresses">{{ p.ips.join(', ') }}</td>
            <td data-label="OS">{{ p.os || '—' }}</td>
            <td class="text-ink-muted" data-label="Last seen">{{ seen(p) }}</td>
            <td class="font-mono text-code" data-label="Path">{{ path(p) }}</td>
            <td class="font-mono text-code" data-label="Routes">
              {{ p.routes.join(', ') || '—' }}
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
