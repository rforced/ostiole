<script setup>
import { Plus } from '@lucide/vue'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import V6ServerDialog from '@/views/services/dhcp/V6ServerDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const dhcp = computed(() => config.ensureServices().dhcp)
/** Interfaces that are switched off. Nothing is advertised on one of them. */
const off = computed(() => new Set(config.interfaces.filter((i) => !i.enabled).map((i) => i.name)))
const editing = ref(null)
const open = ref(false)

const MODES = {
  slaac: 'SLAAC only',
  stateless: 'Stateless DHCPv6',
  managed: 'Managed DHCPv6',
}

function add() {
  editing.value = null
  open.value = true
}
function edit(s) {
  editing.value = s
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Interfaces"
      :count="(dhcp.v6 ?? []).length"
      intro="Router advertisements are sent only while DHCP is enabled."
      flush
    >
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Advertise IPv6
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Interface</th>
            <th>Mode</th>
            <th>Range</th>
            <th>Lease</th>
            <th>DNS</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!(dhcp.v6 ?? []).length">
            <td colspan="6" class="text-ink-muted">
              No IPv6 advertisements. Add one per interface that should serve IPv6.
            </td>
          </tr>
          <tr
            v-for="s in dhcp.v6"
            :key="s.interface"
            :class="{
              'opacity-50': !s.enabled || off.has(s.interface),
              'row-changed': config.isChanged('services.dhcp.v6', s.interface),
            }"
          >
            <td class="font-mono" data-label="">
              <span class="inline-flex items-center gap-1.5">
                {{ s.interface }}
                <span v-if="off.has(s.interface)" class="badge">interface off</span>
                <span v-if="s.dnsRegistration" class="badge">DNS registration</span>
              </span>
            </td>
            <td data-label="Mode">{{ MODES[s.mode] ?? s.mode }}</td>
            <td class="font-mono text-code" data-label="Range">
              <template v-if="s.mode === 'managed'">{{ s.rangeStart }} – {{ s.rangeEnd }}</template>
              <span v-else class="text-ink-muted">—</span>
            </td>
            <td class="font-mono text-code" data-label="Lease">{{ s.leaseTime || '24h' }}</td>
            <td class="font-mono text-code" data-label="DNS">
              {{ s.dns?.join(', ') || 'this router' }}
            </td>
            <td class="actions" data-label="">
              <button type="button" class="link-action" @click="edit(s)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Stop advertising IPv6 on ${s.interface}?`"
                confirm-label="Stop"
                @confirm="config.removeV6Server(s.interface)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <V6ServerDialog v-model:open="open" :server="editing" />
  </div>
</template>
