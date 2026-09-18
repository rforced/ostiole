<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
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

function seen(peer) {
  if (!peer.lastSeen) return '—'
  return new Date(peer.lastSeen).toLocaleString()
}
</script>

<template>
  <div class="space-y-3">
    <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    <p v-if="load.error.value" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ load.error.value }}
    </p>
    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
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
            <td colspan="6" class="text-neutral-500">
              {{ load.updatedAt.value ? 'Nothing to show.' : 'Reading the tailnet…' }}
            </td>
          </tr>
          <tr v-else-if="!status.peers.length" key="empty" class="row-static">
            <td colspan="6" class="text-neutral-500">No peers.</td>
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
            <td class="text-neutral-500">{{ seen(p) }}</td>
            <td class="font-mono text-code">
              {{ p.directAddr || (p.relay ? `relay ${p.relay}` : '—') }}
            </td>
            <td class="font-mono text-code">{{ p.routes.join(', ') || '—' }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
  </div>
</template>
