<script setup>
import { computed, ref } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** Live links usable as a VLAN parent. */
  parents: { type: Array, required: true },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['created'])

const config = useConfigStore()
const parent = ref('')
const id = ref(100)
const error = ref('')

const name = computed(() => (parent.value ? `${parent.value}.${id.value}` : ''))

function create() {
  error.value = ''
  if (!parent.value) return
  if (id.value < 1 || id.value > 4094) {
    error.value = 'VLAN ID must be 1-4094.'
    return
  }
  if (config.findInterface(name.value)) {
    error.value = `${name.value} is already configured.`
    return
  }
  const iface = {
    name: name.value,
    enabled: true,
    vlan: { parent: parent.value, id: id.value },
    ipv4: { mode: 'none' },
    ipv6: { mode: 'none' },
  }
  config.upsertInterface(iface)
  emit('created', iface)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    title="Add VLAN"
    description="Creates an 802.1Q sub-interface. Give it an address and a zone next — a zone of its own if it should have its own firewall rules."
  >
    <form class="space-y-4" @submit.prevent="create">
      <FormField id="vlan-parent" label="Parent interface">
        <select id="vlan-parent" v-model="parent" class="input" required>
          <option value="" disabled>Choose</option>
          <option v-for="p in props.parents" :key="p.name" :value="p.name">{{ p.name }}</option>
        </select>
      </FormField>
      <FormField id="vlan-id" label="VLAN ID" :hint="name ? `Will be named ${name}` : ''">
        <input
          id="vlan-id"
          v-model.number="id"
          type="number"
          min="1"
          max="4094"
          class="input w-32"
          required
        />
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Add to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
