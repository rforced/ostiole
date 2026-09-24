<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import StaticLeaseDialog from '@/views/services/dhcp/StaticLeaseDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const leases = ref([])
const open = ref(false)
const editing = ref(null)
const prefill = ref(null)

/** Pins a client to the address it has. One already pinned in the draft opens as it is. */
function makeStatic(l) {
  const mac = l.mac.toLowerCase()
  editing.value =
    (config.draft?.services?.dhcp?.staticLeases ?? []).find((s) => s.mac.toLowerCase() === mac) ??
    null
  prefill.value = { mac, ip: l.ip, hostname: l.hostname ?? '' }
  open.value = true
}

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
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Address</th>
            <th>Client</th>
            <th>Hostname</th>
            <th>Expires</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!leases.length">
            <td colspan="5" class="text-ink-muted">
              {{ load.updatedAt.value ? 'No leases.' : 'Reading…' }}
            </td>
          </tr>
          <tr v-for="l in leases" :key="l.ip + (l.mac || l.clientId)">
            <td class="font-mono text-code" data-label="Address">
              {{ l.ip }}
              <span v-if="l.family === 6" class="badge ml-1">v6</span>
            </td>
            <!-- DHCPv6 identifies clients by DUID, so there is no MAC. -->
            <td class="font-mono text-code" data-label="Client">
              {{ l.mac || l.clientId || '—' }}
            </td>
            <td class="font-mono text-code" data-label="Hostname">{{ l.hostname }}</td>
            <td data-label="Expires">
              {{ l.static ? 'static' : new Date(l.expires).toLocaleString() }}
            </td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button
                v-if="l.mac && !l.static && l.family !== 6 && !auth.readOnly"
                type="button"
                class="link"
                @click="makeStatic(l)"
              >
                Make static
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <StaticLeaseDialog v-model:open="open" :lease="editing" :prefill="prefill" />
  </div>
</template>
