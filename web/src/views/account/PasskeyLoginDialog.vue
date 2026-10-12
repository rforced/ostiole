<script setup>
import { ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import { errorMessage } from '@/lib/async'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['done'])

const password = ref('')
const error = ref('')
const busy = ref(false)

watch(
  open,
  (on) => {
    if (!on) return
    password.value = ''
    error.value = ''
  },
  { immediate: true },
)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const mfa = await api.auth.setPasskeyLogin(true, password.value)
    emit('done', mfa)
    open.value = false
  } catch (e) {
    error.value = e instanceof ApiError && e.status === 401 ? 'Password is wrong.' : errorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :read-only="false"
    title="Turn on passkey sign-in"
    description="A passkey then signs in without the password."
  >
    <form id="passkey-login-form" class="space-y-4" @submit.prevent="submit">
      <FormField id="passkey-login-password" label="Password">
        <input
          id="passkey-login-password"
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          class="input"
        />
      </FormField>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <ActionButton
        type="submit"
        form="passkey-login-form"
        kind="primary"
        label="Turn on"
        busy-label="Turning on…"
        :busy="busy"
        :disabled="!password"
      />
    </template>
  </AppDialog>
</template>
