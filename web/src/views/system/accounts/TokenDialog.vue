<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'
import { ROLES } from '@/views/system/accounts/roles'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['create'])

const config = useConfigStore()
const form = ref({ name: '', role: 'viewer', expiresInDays: 0, certificates: [] })

watch(open, (on) => {
  if (on) form.value = { name: '', role: 'viewer', expiresInDays: 0, certificates: [] }
})

/** The parent closes the dialog once the server has minted the secret. */
function submit() {
  emit('create', { ...form.value })
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    title="Add token"
    description="The secret is shown once and never stored."
  >
    <form class="space-y-4" @submit.prevent="submit">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="tok-name" label="Name">
          <input
            id="tok-name"
            v-model="form.name"
            class="input"
            required
            placeholder="monitoring"
          />
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
        <select id="tok-role" v-model="form.role" class="input">
          <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
        </select>
      </FormField>
      <FormField
        v-if="config.certificates.length"
        id="tok-certs"
        label="Limit to certificates"
        hint="The token fetches these and can do nothing else."
      >
        <div id="tok-certs" class="flex flex-wrap gap-3">
          <label v-for="c in config.certificates" :key="c.id" class="flex items-center gap-2">
            <input
              v-model="form.certificates"
              type="checkbox"
              class="size-4 rounded"
              :value="c.id"
            />
            <span class="font-mono text-code">{{ c.id }}</span>
          </label>
        </div>
      </FormField>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.name">Create token</button>
      </div>
    </form>
  </AppDialog>
</template>
