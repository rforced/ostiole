<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  zone: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())
const error = ref('')

function blank() {
  return { name: '', description: '', external: false, antiLockout: false, logDrops: false }
}

watch(
  () => [open.value, props.zone],
  () => {
    if (open.value) form.value = { ...blank(), ...(props.zone ?? {}) }
    error.value = ''
  },
  { immediate: true },
)

function save() {
  error.value = ''
  const name = form.value.name.trim()
  if (!/^[a-z][a-z0-9_]{0,30}$/.test(name)) {
    error.value = 'Name must be lowercase letters, digits, or underscores and start with a letter.'
    return
  }
  const previous = props.zone?.name ?? name
  if (name !== previous && config.zones.some((z) => z.name === name)) {
    error.value = `Zone ${name} already exists.`
    return
  }
  const out = { ...form.value, name }
  for (const k of ['description']) if (!out[k]) delete out[k]
  for (const k of ['external', 'antiLockout', 'logDrops']) if (!out[k]) delete out[k]
  config.upsertZone(out, previous)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="zone ? `Zone ${zone.name}` : 'New zone'">
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="zone-name" label="Name">
          <input
            id="zone-name"
            v-model="form.name"
            class="input font-mono"
            autocapitalize="none"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField id="zone-desc" label="Description">
          <input id="zone-desc" v-model="form.description" class="input" />
        </FormField>
      </div>
      <div class="space-y-2 text-sm">
        <label class="flex items-start gap-2">
          <input
            v-model="form.external"
            type="checkbox"
            class="mt-0.5 size-4 rounded border-neutral-300"
          />
          <span
            ><span class="font-medium">External</span> — internet-facing; IPv4 leaving it is
            masqueraded when outbound NAT is automatic.</span
          >
        </label>
        <label class="flex items-start gap-2">
          <input
            v-model="form.antiLockout"
            type="checkbox"
            class="mt-0.5 size-4 rounded border-neutral-300"
          />
          <span
            ><span class="font-medium">Anti-lockout</span> — always allow the management ports from
            this zone.</span
          >
        </label>
        <label class="flex items-start gap-2">
          <input
            v-model="form.logDrops"
            type="checkbox"
            class="mt-0.5 size-4 rounded border-neutral-300"
          />
          <span
            ><span class="font-medium">Log drops</span> — log packets that match no rule in this
            zone.</span
          >
        </label>
      </div>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
