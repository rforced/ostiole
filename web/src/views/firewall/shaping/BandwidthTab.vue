<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { formatRate } from '@/lib/format'
import { useConfigStore } from '@/stores/config'
import BandwidthDialog from '@/views/firewall/shaping/BandwidthDialog.vue'

/** The label the dialog offers, so the table reads the same as the form. */
const LINKS = {
  ethernet: 'Ethernet or fibre',
  docsis: 'Cable',
  'pppoe-ptm': 'VDSL with PPPoE',
  'bridged-ptm': 'VDSL',
  'pppoe-vcmux': 'ADSL with PPPoE',
  conservative: 'Not sure',
}

const config = useConfigStore()
const editing = ref(null)
const open = ref(false)

const shaped = computed(() => config.shapedInterfaces)

function add() {
  editing.value = null
  open.value = true
}

function edit(iface) {
  editing.value = iface
  open.value = true
}

function rate(bits) {
  return bits ? formatRate(bits) : 'not shaped'
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Speeds"
      :count="shaped.length"
      intro="Download and upload are read from this router's point of view, whichever way the
        interface faces."
      flush
    >
      <template #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Set a speed
        </button>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Interface</th>
            <th>Zone</th>
            <th>Download</th>
            <th>Upload</th>
            <th>Link type</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!shaped.length" key="empty" class="row-static">
            <td colspan="6" class="text-ink-muted">
              No interface has a speed. Queues are first come, first served.
            </td>
          </tr>
          <tr
            v-for="i in shaped"
            :key="i.name"
            :class="{ 'row-changed': config.isChanged('interfaces', i.name) }"
          >
            <td class="font-mono">{{ i.name }}</td>
            <td class="font-mono text-code">{{ i.zone || '—' }}</td>
            <td class="font-mono text-code tabular-nums">{{ rate(i.shaping.download) }}</td>
            <td class="font-mono text-code tabular-nums">{{ rate(i.shaping.upload) }}</td>
            <td>{{ LINKS[i.shaping.link || 'ethernet'] }}</td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(i)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Stop shaping ${i.name}?`"
                description="The queue goes back to whatever the kernel does on its own."
                @confirm="config.clearShaping(i.name)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <BandwidthDialog v-model:open="open" :iface="editing" />
  </div>
</template>
