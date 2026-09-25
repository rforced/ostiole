<script setup>
import { LoaderCircle, Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { deviceName, useWake } from '@/lib/wol'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import DeviceDialog from '@/views/services/wol/DeviceDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const { busy, errors, wake, wakeAll } = useWake()

const devices = computed(() => config.wolDevices)
/** Interfaces that are switched off. Nothing is sent on one of them. */
const off = computed(() => new Set(config.interfaces.filter((i) => !i.enabled).map((i) => i.name)))

const open = ref(false)
/** The device the dialog edits; null adds one. */
const editing = ref(null)

function add() {
  editing.value = null
  open.value = true
}
function edit(d) {
  editing.value = d
  open.value = true
}
</script>

<template>
  <SectionCard title="Devices" :count="devices.length" flush>
    <template v-if="!auth.readOnly" #actions>
      <button
        v-if="devices.length > 1"
        type="button"
        class="btn-secondary"
        :disabled="busy !== ''"
        :aria-busy="busy === '*'"
        @click="wakeAll(devices.filter((d) => !off.has(d.interface)))"
      >
        <LoaderCircle v-if="busy === '*'" class="size-4 animate-spin" aria-hidden="true" />
        {{ busy === '*' ? 'Waking…' : 'Wake all' }}
      </button>
      <button type="button" class="btn-secondary" @click="add">
        <Plus class="size-4" aria-hidden="true" /> Add device
      </button>
    </template>
    <div v-if="errors.length" class="card-strip" role="alert">
      <p v-for="e in errors" :key="e" class="text-bad">{{ e }}</p>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Device</th>
          <th>MAC</th>
          <th>Interface</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!devices.length">
          <td colspan="4" class="text-ink-muted">No devices.</td>
        </tr>
        <tr
          v-for="d in devices"
          :key="d.id"
          :class="{ 'row-changed': config.isChanged('services.wol.devices', d.id) }"
        >
          <td data-label="">{{ deviceName(d) }}</td>
          <td class="font-mono text-code" data-label="MAC">{{ d.mac }}</td>
          <td class="font-mono" data-label="Interface">
            {{ d.interface }}
            <span v-if="off.has(d.interface)" class="badge ml-1">interface off</span>
          </td>
          <td class="text-right whitespace-nowrap" data-label="">
            <button
              v-if="!auth.readOnly"
              type="button"
              class="link"
              :disabled="busy !== '' || off.has(d.interface)"
              :aria-busy="busy === d.id"
              @click="wake(d, deviceName(d), d.id)"
            >
              Wake
            </button>
            <button type="button" class="link ml-3" @click="edit(d)">
              {{ auth.readOnly ? 'View' : 'Edit' }}
            </button>
            <ConfirmButton
              class="ml-3"
              label="Delete"
              :question="`Delete ${deviceName(d)}?`"
              :dependents="config.wolDeviceDependents(d.id)"
              @confirm="config.removeWoLDevice(d)"
            />
          </td>
        </tr>
      </tbody>
    </table>
    <DeviceDialog v-model:open="open" :device="editing" />
  </SectionCard>
</template>
