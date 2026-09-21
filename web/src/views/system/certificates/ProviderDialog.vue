<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The provider being edited, or null for a new one. */
  provider: { type: Object, default: null },
  /** The kinds this build can write to, with their fields. */
  kinds: { type: Array, default: () => [] },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()

const blank = () => ({
  id: '',
  description: '',
  kind: props.kinds[0]?.kind ?? '',
  settings: {},
  propagationSeconds: 0,
})

const form = ref(blank())
watch(
  () => [open.value, props.provider, props.kinds],
  () => {
    if (!open.value) return
    const p = props.provider
    form.value = p ? { ...blank(), ...p, settings: { ...(p.settings ?? {}) } } : blank()
  },
  { immediate: true },
)

/** The fields the chosen kind takes; the dialog is built from them. */
const fields = computed(() => props.kinds.find((k) => k.kind === form.value.kind)?.fields ?? [])

const valid = computed(
  () =>
    Boolean(form.value.id) &&
    Boolean(form.value.kind) &&
    fields.value.every((f) => !f.required || form.value.settings[f.key]),
)

function save() {
  const f = form.value
  const out = { id: f.id.trim(), kind: f.kind, settings: {} }
  if (f.description) out.description = f.description
  // Only the chosen kind's fields go into the draft; a setting left over
  // from another kind is refused by the server.
  for (const field of fields.value) {
    const value = f.settings[field.key]
    if (value) out.settings[field.key] = value
  }
  if (f.propagationSeconds) out.propagationSeconds = Number(f.propagationSeconds)
  config.upsertDnsProvider(out, props.provider?.id)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="provider ? `Provider ${provider.id}` : 'Add DNS provider'">
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="prov-id" label="Name">
          <input
            id="prov-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
            :disabled="Boolean(provider)"
          />
        </FormField>
        <FormField id="prov-desc" label="Description">
          <input id="prov-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <FormField id="prov-kind" label="Kind">
        <select id="prov-kind" v-model="form.kind" class="input">
          <option v-for="k in kinds" :key="k.kind" :value="k.kind">{{ k.label }}</option>
        </select>
      </FormField>

      <FormField
        v-for="f in fields"
        :id="`prov-${f.key}`"
        :key="f.key"
        :label="f.label"
        :hint="f.hint"
      >
        <input
          :id="`prov-${f.key}`"
          v-model="form.settings[f.key]"
          :type="f.secret ? 'password' : 'text'"
          class="input font-mono"
          :required="f.required"
          autocomplete="off"
          spellcheck="false"
        />
      </FormField>

      <FormField id="prov-wait" label="Propagation wait" hint="Seconds. 0 is the provider's own.">
        <input
          id="prov-wait"
          v-model.number="form.propagationSeconds"
          type="number"
          min="0"
          max="600"
          class="input w-24 font-mono"
        />
      </FormField>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!valid">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
