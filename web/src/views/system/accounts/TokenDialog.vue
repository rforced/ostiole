<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useConfigStore } from '@/stores/config'
import { ROLES } from '@/views/system/accounts/roles'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['create'])

const config = useConfigStore()
const blank = () => ({
  name: '',
  role: 'viewer',
  expiresInDays: 0,
  certificates: [],
  metrics: false,
})
const form = ref(blank())

watch(open, (on) => {
  if (on) form.value = blank()
})

/** A token for a scraper is a viewer, and fetches no certificates too. */
watch(
  () => form.value.metrics,
  (on) => {
    if (!on) return
    form.value.role = 'viewer'
    form.value.certificates = []
  },
)

/** The parent closes the dialog once the server has minted the secret. */
function submit() {
  const { metrics, ...token } = form.value
  emit('create', metrics ? { ...token, metrics } : token)
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    title="Add token"
    description="The secret is shown once and never stored."
  >
    <form id="token-form" class="space-y-4" @submit.prevent="submit">
      <div class="fields">
        <FormField id="tok-name" label="Name">
          <input id="tok-name" v-model="form.name" class="input" required />
        </FormField>
        <FormField id="tok-days" label="Expires after (days)" hint="0 never expires.">
          <input
            id="tok-days"
            v-model.number="form.expiresInDays"
            type="number"
            min="0"
            max="3650"
            class="input w-24 font-mono max-sm:w-full"
          />
        </FormField>
      </div>
      <FormField id="tok-role" label="Role">
        <select id="tok-role" v-model="form.role" class="input" :disabled="form.metrics">
          <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
        </select>
      </FormField>
      <ToggleRow
        v-model="form.metrics"
        label="Metrics only"
        hint="For a monitoring system: the token reads /metrics and nothing else, the logs included."
      />
      <FormField
        v-if="config.certificates.length && !form.metrics"
        id="tok-certs"
        label="Limit to certificates"
        hint="The token fetches these and can do nothing else."
      >
        <div id="tok-certs" class="flex flex-wrap gap-3">
          <label v-for="c in config.certificates" :key="c.id" class="flex items-center gap-2">
            <input v-model="form.certificates" type="checkbox" class="checkbox" :value="c.id" />
            <span class="font-mono text-code">{{ c.id }}</span>
          </label>
        </div>
      </FormField>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="token-form" class="btn-primary" :disabled="!form.name">
        Create token
      </button>
    </template>
  </AppDialog>
</template>
