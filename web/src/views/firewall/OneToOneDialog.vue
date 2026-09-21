<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ entry: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
// Declared before form: blank() reads it while the ref is created.
const externalZones = computed(() => config.zones.filter((z) => z.external))
const form = ref(blank())

function blank() {
  return {
    id: '',
    description: '',
    enabled: true,
    zone: externalZones.value[0]?.name ?? '',
    external: '',
    internal: '',
  }
}

watch(
  () => [open.value, props.entry],
  () => {
    if (open.value) form.value = props.entry ? { ...blank(), ...props.entry } : blank()
  },
  { immediate: true },
)

function save() {
  const f = form.value
  const out = {
    id: f.id || newId('one'),
    enabled: f.enabled,
    zone: f.zone,
    external: f.external.trim(),
    internal: f.internal.trim(),
  }
  if (f.description) out.description = f.description
  config.upsertOneToOne(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="entry ? `1:1 NAT ${entry.id}` : 'Add 1:1 NAT'"
    description="One outside address stands in for one host inside."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="one-desc" label="Description">
        <input id="one-desc" v-model="form.description" class="input" />
      </FormField>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="one-zone" label="External zone">
          <select id="one-zone" v-model="form.zone" class="input" required>
            <option v-for="z in externalZones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
        <FormField
          id="one-ext"
          label="External address"
          hint="A second address on the WAN, or one routed to this firewall."
        >
          <input
            id="one-ext"
            v-model="form.external"
            class="input font-mono"
            required
            spellcheck="false"
            placeholder="203.0.113.10"
          />
        </FormField>
        <FormField id="one-int" label="Internal address">
          <input
            id="one-int"
            v-model="form.internal"
            class="input font-mono"
            required
            spellcheck="false"
            placeholder="10.0.0.25"
          />
        </FormField>
      </div>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.zone">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
