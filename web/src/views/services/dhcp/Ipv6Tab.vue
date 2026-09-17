<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useConfigStore } from '@/stores/config'
import V6ServerDialog from '@/views/services/dhcp/V6ServerDialog.vue'

const config = useConfigStore()
const dhcp = computed(() => config.ensureServices().dhcp)
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
  <div class="space-y-4">
    <p class="text-sm text-neutral-500">
      Router advertisements are sent only while the DHCP server is enabled.
    </p>

    <div class="flex items-center gap-3">
      <h2 id="v6-title" class="section-title">Interfaces</h2>
      <button type="button" class="btn-secondary" @click="add">
        <Plus class="mr-1 size-4" aria-hidden="true" /> Advertise IPv6
      </button>
    </div>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
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
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!(dhcp.v6 ?? []).length" key="empty" class="row-static">
            <td colspan="6" class="text-neutral-500">
              No IPv6 advertisements. Add one per interface that should serve IPv6.
            </td>
          </tr>
          <tr
            v-for="s in dhcp.v6"
            :key="s.interface"
            :class="{
              'opacity-50': !s.enabled,
              'row-changed': config.isChanged('services.dhcp.v6', s.interface),
            }"
          >
            <td class="font-mono">{{ s.interface }}</td>
            <td>{{ MODES[s.mode] ?? s.mode }}</td>
            <td class="font-mono text-code">
              <template v-if="s.mode === 'managed'">{{ s.rangeStart }} – {{ s.rangeEnd }}</template>
              <span v-else class="text-neutral-500">—</span>
            </td>
            <td class="font-mono text-code">{{ s.leaseTime || '12h' }}</td>
            <td class="font-mono text-code">{{ s.dns?.join(', ') || 'this router' }}</td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(s)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Stop advertising IPv6 on ${s.interface}?`"
                confirm-label="Stop"
                @confirm="config.removeV6Server(s.interface)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </div>

    <V6ServerDialog v-model:open="open" :server="editing" />
  </div>
</template>
