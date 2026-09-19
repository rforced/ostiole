<script setup>
import { computed, ref } from 'vue'
import { Plus } from 'lucide-vue-next'

import ConfirmButton from '@/components/ConfirmButton.vue'
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
  <div class="space-y-3">
    <button type="button" class="btn-secondary" :disabled="!config.radios.length" @click="add">
      <Plus class="mr-1 size-4" aria-hidden="true" /> Add network
    </button>
    <p v-if="!config.radios.length" class="text-sm text-neutral-500">Configure a radio first.</p>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
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
            <td colspan="6" class="text-neutral-500">No networks.</td>
          </tr>
          <tr
            v-for="row in rows"
            :key="row.iface.name"
            :class="{ 'row-changed': config.isChanged('interfaces', row.iface.name) }"
          >
            <td>
              <div class="font-medium">{{ row.iface.wireless.ssid }}</div>
              <div v-if="row.iface.wireless.hidden" class="text-xs text-neutral-500">hidden</div>
            </td>
            <td class="font-mono text-code">{{ row.iface.wireless.radio }}</td>
            <td>{{ SECURITY[row.iface.wireless.security] ?? row.iface.wireless.security }}</td>
            <td>{{ row.attach }}</td>
            <td class="font-mono text-code">
              {{ row.iface.name
              }}<span v-if="!row.iface.enabled" class="ml-1 text-neutral-500">(disabled)</span>
            </td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(row.iface)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Remove"
                :question="`Remove ${row.iface.wireless.ssid} from the configuration?`"
                description="It stops being served on the next apply."
                :dependents="config.interfaceDependents(row.iface.name)"
                dependents-label="Also removed"
                :typed="row.iface.name"
                @confirm="config.removeInterface(row.iface.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <NetworkDialog v-model:open="dialogOpen" :network="editing" />
  </div>
</template>
