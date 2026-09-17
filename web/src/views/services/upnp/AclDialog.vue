<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  entry: { type: Object, default: null },
  /** Position in the list; below zero adds a new entry. */
  index: { type: Number, default: -1 },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return {
    action: 'allow',
    externalPorts: '1024-65535',
    source: '',
    internalPorts: '1024-65535',
    description: '',
  }
}

watch(
  () => [open.value, props.entry],
  () => {
    if (open.value) form.value = { ...blank(), ...(props.entry ?? {}) }
  },
  { immediate: true },
)

function save() {
  const f = form.value
  const out = {
    action: f.action,
    externalPorts: f.externalPorts.trim(),
    source: f.source.trim(),
    internalPorts: f.internalPorts.trim(),
  }
  if (f.description) out.description = f.description
  config.upsertUPnPRule(out, props.index)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="entry ? 'Access list entry' : 'New access list entry'"
    description="The first entry that matches the request decides it."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="acl-action" label="Action">
          <select id="acl-action" v-model="form.action" class="input">
            <option value="allow">Allow</option>
            <option value="deny">Deny</option>
          </select>
        </FormField>
        <FormField
          id="acl-source"
          label="Client"
          hint="One address or a range, e.g. 192.168.1.0/24."
        >
          <input
            id="acl-source"
            v-model="form.source"
            class="input font-mono"
            required
            spellcheck="false"
            placeholder="192.168.1.0/24"
          />
        </FormField>
        <FormField id="acl-ext" label="External ports" hint="A port or a range, e.g. 1024-65535.">
          <input
            id="acl-ext"
            v-model="form.externalPorts"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="acl-int" label="Internal ports" hint="A port or a range, e.g. 1024-65535.">
          <input
            id="acl-int"
            v-model="form.internalPorts"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="acl-desc" label="Description" class="sm:col-span-2">
          <input id="acl-desc" v-model="form.description" class="input" />
        </FormField>
      </div>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.source">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
