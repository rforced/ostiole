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
    await api.auth.secondFactorClear(password.value)
    emit('done')
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
    title="Turn off the second factor"
    description="Removes the authenticator app and every passkey. Sign-in asks for the password alone."
  >
    <form id="second-factor-off-form" class="space-y-4" @submit.prevent="submit">
      <FormField id="second-factor-password" label="Password">
        <input
          id="second-factor-password"
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
        form="second-factor-off-form"
        kind="danger"
        label="Turn off"
        busy-label="Turning off…"
        :busy="busy"
        :disabled="!password"
      />
    </template>
  </AppDialog>
</template>
