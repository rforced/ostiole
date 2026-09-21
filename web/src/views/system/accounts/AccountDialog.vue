<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { ROLES } from '@/views/system/accounts/roles'

/** The shortest password the server will take. */
const MIN_PASSWORD = 12

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['create'])

const form = ref({ username: '', password: '', role: 'viewer' })
const valid = computed(
  () => form.value.username !== '' && form.value.password.length >= MIN_PASSWORD,
)

watch(open, (on) => {
  if (on) form.value = { username: '', password: '', role: 'viewer' }
})

/** The parent closes the dialog once the server has taken the account. */
function submit() {
  emit('create', { ...form.value })
}
</script>

<template>
  <AppDialog v-model:open="open" title="Add account" description="They can sign in at once.">
    <form class="space-y-4" @submit.prevent="submit">
      <FormField id="user-name" label="Username">
        <input
          id="user-name"
          v-model="form.username"
          class="input font-mono"
          autocomplete="off"
          required
          placeholder="operator"
        />
      </FormField>
      <FormField id="user-password" label="Password" :hint="`At least ${MIN_PASSWORD} characters.`">
        <input
          id="user-password"
          v-model="form.password"
          type="password"
          autocomplete="new-password"
          class="input"
          :minlength="MIN_PASSWORD"
          required
        />
      </FormField>
      <FormField id="user-role" label="Role">
        <select id="user-role" v-model="form.role" class="input">
          <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
        </select>
      </FormField>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!valid">Create account</button>
      </div>
    </form>
  </AppDialog>
</template>
