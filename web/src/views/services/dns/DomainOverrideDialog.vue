<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The override being edited, or null for a new one. */
  override: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()

const form = ref({ domain: '', servers: '', description: '' })
watch(open, (on) => {
  if (!on) return
  const d = props.override
  form.value = {
    domain: d?.domain ?? '',
    servers: (d?.servers ?? []).join(', '),
    description: d?.description ?? '',
  }
})

const title = computed(() =>
  props.override ? `Domain ${props.override.domain}` : 'Add domain override',
)

function save() {
  const out = { domain: form.value.domain.trim(), servers: parseList(form.value.servers) }
  if (form.value.description) out.description = form.value.description
  config.upsertDomainOverride(out, props.override?.domain ?? out.domain)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="title">
    <form id="domain-override-form" class="space-y-4" @submit.prevent="save">
      <FormField id="do-domain" label="Domain" hint="Subdomains follow it, like ts.net.">
        <input
          id="do-domain"
          v-model="form.domain"
          class="input font-mono"
          required
          spellcheck="false"
        />
      </FormField>
      <FormField
        id="do-servers"
        label="Resolvers"
        hint="Comma separated. Add #port for anything but 53."
      >
        <input
          id="do-servers"
          v-model="form.servers"
          class="input font-mono"
          required
          spellcheck="false"
        />
      </FormField>
      <FormField id="do-desc" label="Description">
        <input id="do-desc" v-model="form.description" class="input" />
      </FormField>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="domain-override-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
