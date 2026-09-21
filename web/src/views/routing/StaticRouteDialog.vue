<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { newId } from '@/lib/ids'
import { interfaceLabel } from '@/lib/interfaces'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ route: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })
const config = useConfigStore()

function blank() {
  return { id: '', description: '', enabled: true, destination: '', gateway: '', interface: '' }
}
const form = ref(blank())

watch(
  () => [open.value, props.route],
  () => {
    if (open.value) form.value = props.route ? { ...blank(), ...props.route } : blank()
  },
  { immediate: true },
)

function save() {
  const f = form.value
  const out = {
    id: f.id || newId('rt'),
    enabled: f.enabled,
    destination: f.destination.trim(),
    gateway: f.gateway.trim(),
  }
  if (f.description) out.description = f.description
  if (f.interface) out.interface = f.interface
  config.upsertRoute(out)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="route ? `Route ${route.id}` : 'Add static route'">
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="rt-desc" label="Description">
        <input id="rt-desc" v-model="form.description" class="input" />
      </FormField>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="rt-dest" label="Destination network">
          <input
            id="rt-dest"
            v-model="form.destination"
            class="input font-mono"
            placeholder="10.200.0.0/16"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="rt-gw" label="Gateway">
          <input
            id="rt-gw"
            v-model="form.gateway"
            class="input font-mono"
            placeholder="10.10.0.254"
            required
            spellcheck="false"
          />
        </FormField>
      </div>
      <FormField id="rt-if" label="Interface" hint="Empty picks it from the gateway's network.">
        <select id="rt-if" v-model="form.interface" class="input">
          <option value="">Automatic</option>
          <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
            {{ interfaceLabel(i) }}
          </option>
        </select>
      </FormField>
      <ToggleRow v-model="form.enabled" label="Enabled" />
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
