<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { interfaceLabel } from '@/lib/interfaces'
import { deviceName, wakeInterfaces } from '@/lib/wol'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The device being edited, or null to add one. */
  device: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })
const config = useConfigStore()
const error = ref('')
const form = ref({ description: '', mac: '', interface: '' })

/**
 * The draft's inside interfaces with Ethernet under them. One the device
 * already names stays on offer, so an edit shows where it is even after
 * that stopped being allowed.
 */
const choices = computed(() => {
  const list = wakeInterfaces(config.draft)
  const current = props.device && config.findInterface(props.device.interface)
  return current && !list.some((i) => i.name === current.name) ? [...list, current] : list
})

watch(
  () => [open.value, props.device],
  () => {
    if (!open.value) return
    error.value = ''
    form.value = {
      description: '',
      mac: '',
      interface: choices.value[0]?.name ?? '',
      ...props.device,
    }
  },
  { immediate: true },
)

const MAC = /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/

function save() {
  error.value = ''
  // Windows writes a MAC with dashes.
  const mac = form.value.mac.trim().toLowerCase().replaceAll('-', ':')
  if (!MAC.test(mac)) {
    error.value = 'A MAC address looks like aa:bb:cc:dd:ee:ff.'
    return
  }
  if (Number.parseInt(mac.slice(0, 2), 16) & 1) {
    error.value = `${mac} is a group address, not one machine's.`
    return
  }
  if (config.wolDevices.some((d) => d.mac === mac && d.id !== props.device?.id)) {
    error.value = `${mac} is already listed.`
    return
  }
  const out = { id: props.device?.id || newId('wol'), interface: form.value.interface, mac }
  const description = form.value.description.trim()
  if (description) out.description = description
  config.upsertWoLDevice(out, props.device?.id)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="device ? `Device ${deviceName(device)}` : 'Add device'">
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="wol-desc" label="Description">
          <input id="wol-desc" v-model="form.description" class="input" placeholder="Office NAS" />
        </FormField>
        <FormField id="wol-mac" label="MAC address">
          <input
            id="wol-mac"
            v-model="form.mac"
            class="input font-mono"
            placeholder="aa:bb:cc:dd:ee:ff"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField
          id="wol-if"
          label="Interface"
          hint="Only machines on this network see the packet."
        >
          <select id="wol-if" v-model="form.interface" class="input" required>
            <option v-for="i in choices" :key="i.name" :value="i.name">
              {{ interfaceLabel(i) }}{{ i.enabled ? '' : ', off' }}
            </option>
          </select>
        </FormField>
      </div>
      <p v-if="error" role="alert" class="text-sm text-bad">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
