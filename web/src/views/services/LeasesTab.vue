<script setup>
import { RefreshCw } from 'lucide-vue-next'
import { onMounted, ref } from 'vue'

import { api } from '@/lib/api'

const leases = ref([])
const error = ref('')

async function refresh() {
  try {
    leases.value = await api.services.leases()
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
onMounted(refresh)
</script>

<template>
  <div class="space-y-3">
    <button type="button" class="btn-secondary" @click="refresh">
      <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Refresh
    </button>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Address</th>
            <th>Client</th>
            <th>Hostname</th>
            <th>Expires</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!leases.length">
            <td colspan="4" class="text-neutral-500">No leases yet.</td>
          </tr>
          <tr v-for="l in leases" :key="l.ip + (l.mac || l.clientId)">
            <td class="font-mono text-xs">
              {{ l.ip }}
              <span v-if="l.family === 6" class="badge ml-1">v6</span>
            </td>
            <!-- DHCPv6 identifies clients by DUID, so there is no MAC. -->
            <td class="font-mono text-xs">{{ l.mac || l.clientId || '—' }}</td>
            <td class="font-mono text-xs">{{ l.hostname }}</td>
            <td class="text-xs">
              {{ l.static ? 'static' : new Date(l.expires).toLocaleString() }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
