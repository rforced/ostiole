<script setup>
import { computed, ref } from 'vue'
import { Plus } from 'lucide-vue-next'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useConfigStore } from '@/stores/config'
import NetworkDialog from '@/views/wireless/NetworkDialog.vue'

const config = useConfigStore()
const editing = ref(null)
const dialogOpen = ref(false)

const SECURITY = {
  'wpa2-wpa3': 'WPA2 and WPA3',
  wpa3: 'WPA3',
  wpa2: 'WPA2',
  owe: 'Enhanced open',
  open: 'Open',
}

const rows = computed(() =>
  config.wirelessNetworks.map((i) => ({
    iface: i,
    attach: attachment(i),
  })),
)

/** Where a network lands: a port on a bridge, or a zone of its own. */
function attachment(iface) {
  const master = config.interfaces.find((i) => i.bridge?.members?.includes(iface.name))
  if (master) return `port on ${master.name}`
  if (iface.zone) return `zone ${iface.zone}`
  return 'unassigned'
}

function add() {
  editing.value = null
  dialogOpen.value = true
}

function edit(iface) {
  editing.value = iface
  dialogOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Networks" :count="rows.length" flush>
      <template #actions>
        <button type="button" class="btn-secondary" :disabled="!config.radios.length" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add network
        </button>
      </template>
      <div v-if="!config.radios.length" class="card-strip text-ink-muted">
        Configure a radio first.
      </div>
      <table class="table">
        <thead>
          <tr>
            <th>Network</th>
            <th>Radio</th>
            <th>Security</th>
            <th>Attached to</th>
            <th>Interface</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="6" class="text-ink-muted">No networks.</td>
          </tr>
          <tr
            v-for="row in rows"
            :key="row.iface.name"
            :class="{ 'row-changed': config.isChanged('interfaces', row.iface.name) }"
          >
            <td>
              <div class="font-medium">{{ row.iface.wireless.ssid }}</div>
              <div v-if="row.iface.wireless.hidden" class="text-xs text-ink-muted">hidden</div>
            </td>
            <td class="font-mono text-code">{{ row.iface.wireless.radio }}</td>
            <td>{{ SECURITY[row.iface.wireless.security] ?? row.iface.wireless.security }}</td>
            <td>{{ row.attach }}</td>
            <td class="font-mono text-code">
              {{ row.iface.name
              }}<span v-if="!row.iface.enabled" class="ml-1 text-ink-muted">(disabled)</span>
            </td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(row.iface)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete ${row.iface.wireless.ssid} from the configuration?`"
                description="It stops being served on the next apply."
                :dependents="config.interfaceDependents(row.iface.name)"
                dependents-label="Also deleted"
                :typed="row.iface.name"
                @confirm="config.removeInterface(row.iface.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <NetworkDialog v-model:open="dialogOpen" :network="editing" />
  </div>
</template>
