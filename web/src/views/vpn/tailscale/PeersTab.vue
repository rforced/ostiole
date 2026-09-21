<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'

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
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Peers" :count="status?.running ? status.peers.length : 0" flush>
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
      </template>
      <div v-if="load.error.value" class="px-4 pb-3">
        <p role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
      <table class="table">
        <thead>
          <tr>
            <th>Node</th>
            <th>Addresses</th>
            <th>OS</th>
            <th>Last seen</th>
            <th>Path</th>
            <th>Routes</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!status?.running" key="stopped" class="row-static">
            <td colspan="6" class="text-ink-muted">
              {{ load.updatedAt.value ? 'No peers.' : 'Reading…' }}
            </td>
          </tr>
          <tr v-else-if="!status.peers.length" key="empty" class="row-static">
            <td colspan="6" class="text-ink-muted">No peers.</td>
          </tr>
          <tr v-for="p in status?.running ? status.peers : []" :key="p.dnsName || p.hostName">
            <td>
              <div class="font-mono">{{ name(p) }}</div>
              <span v-if="p.online" class="badge badge-ok">online</span>
              <span v-else class="badge">offline</span>
              <span v-if="p.exitNode" class="badge ml-1">exit node</span>
              <span v-if="p.expired" class="badge badge-warn ml-1">expired</span>
            </td>
            <td class="font-mono text-code">{{ p.ips.join(', ') }}</td>
            <td>{{ p.os || '—' }}</td>
            <td class="text-ink-muted">{{ seen(p) }}</td>
            <td class="font-mono text-code">{{ path(p) }}</td>
            <td class="font-mono text-code">{{ p.routes.join(', ') || '—' }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>
  </div>
</template>
