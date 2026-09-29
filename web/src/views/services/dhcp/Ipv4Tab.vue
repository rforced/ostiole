<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { byAddress, byText, useSort } from '@/lib/sort'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ServerDialog from '@/views/services/dhcp/ServerDialog.vue'
import StaticLeaseDialog from '@/views/services/dhcp/StaticLeaseDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const dhcp = computed(() => config.ensureServices().dhcp)
/** Interfaces that are switched off. A pool on one of them hands out nothing. */
const off = computed(() => new Set(config.interfaces.filter((i) => !i.enabled).map((i) => i.name)))
const serverEditing = ref(null)
const serverOpen = ref(false)
const leaseEditing = ref(null)
const leaseOpen = ref(false)

const LEASE_COLUMNS = [
  ['mac', 'MAC'],
  ['ip', 'IPv4'],
  ['ipv6', 'IPv6'],
  ['hostname', 'Hostname'],
  ['description', 'Description'],
]

/** In the order they were added until a header says otherwise. */
const leaseSort = useSort(() => dhcp.value.staticLeases ?? [], {
  mac: byText((l) => l.mac),
  ip: byAddress((l) => l.ip),
  ipv6: byAddress((l) => l.ipv6),
  hostname: byText((l) => l.hostname),
  description: byText((l) => l.description),
})
const staticLeases = leaseSort.sorted

function addServer() {
  serverEditing.value = null
  serverOpen.value = true
}
function editServer(s) {
  serverEditing.value = s
  serverOpen.value = true
}
function addLease() {
  leaseEditing.value = null
  leaseOpen.value = true
}
function editLease(l) {
  leaseEditing.value = l
  leaseOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Servers" :count="(dhcp.servers ?? []).length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="addServer">
          <Plus class="size-4" aria-hidden="true" /> Add server
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Interface</th>
            <th>Range</th>
            <th>Lease</th>
            <th>Gateway</th>
            <th>DNS</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!(dhcp.servers ?? []).length">
            <td colspan="6" class="text-ink-muted">
              No servers. Add one per interface that should hand out addresses.
            </td>
          </tr>
          <tr
            v-for="s in dhcp.servers"
            :key="s.interface"
            :class="{
              'opacity-50': !s.enabled || off.has(s.interface),
              'row-changed': config.isChanged('services.dhcp.servers', s.interface),
            }"
          >
            <td class="font-mono" data-label="">
              {{ s.interface }}
              <span v-if="off.has(s.interface)" class="badge ml-1">interface off</span>
              <span v-if="s.dnsRegistration" class="badge ml-1">DNS registration</span>
            </td>
            <td class="font-mono text-code" data-label="Range">
              {{ s.rangeStart }} – {{ s.rangeEnd }}
            </td>
            <td class="font-mono text-code" data-label="Lease">{{ s.leaseTime || '24h' }}</td>
            <td class="font-mono text-code" data-label="Gateway">
              {{ s.gateway || 'this router' }}
            </td>
            <td class="font-mono text-code" data-label="DNS">
              {{ s.dns?.join(', ') || 'this router' }}
            </td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button type="button" class="link" @click="editServer(s)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete the DHCP server on ${s.interface}?`"
                @confirm="config.removeServer(s.interface)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <SectionCard title="Static leases" :count="(dhcp.staticLeases ?? []).length" flush>
      <template #actions>
        <SortSelect :sort="leaseSort" :columns="LEASE_COLUMNS" />
        <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="addLease">
          <Plus class="size-4" aria-hidden="true" /> Add static lease
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <SortHeader by="mac" :sort="leaseSort">MAC</SortHeader>
            <SortHeader by="ip" :sort="leaseSort">IPv4</SortHeader>
            <SortHeader by="ipv6" :sort="leaseSort">IPv6</SortHeader>
            <SortHeader by="hostname" :sort="leaseSort">Hostname</SortHeader>
            <SortHeader by="description" :sort="leaseSort">Description</SortHeader>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!(dhcp.staticLeases ?? []).length">
            <td colspan="6" class="text-ink-muted">No static leases.</td>
          </tr>
          <tr
            v-for="l in staticLeases"
            :key="l.mac"
            :class="{ 'row-changed': config.isChanged('services.dhcp.staticLeases', l.mac) }"
          >
            <td class="font-mono text-code" data-label="MAC">
              {{ l.mac }}
              <RandomMacBadge :mac="l.mac" />
            </td>
            <td class="font-mono text-code" data-label="IPv4">{{ l.ip || '—' }}</td>
            <td class="font-mono text-code" data-label="IPv6">{{ l.ipv6 || '—' }}</td>
            <td class="font-mono text-code" data-label="Hostname">{{ l.hostname }}</td>
            <td data-label="Description">{{ l.description }}</td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button type="button" class="link" @click="editLease(l)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete the static lease for ${l.mac}?`"
                :description="l.description"
                @confirm="config.removeStaticLease(l.mac)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <ServerDialog v-model:open="serverOpen" :server="serverEditing" />
    <StaticLeaseDialog v-model:open="leaseOpen" :lease="leaseEditing" />
  </div>
</template>
