<script setup lang="ts">
import type { PropType } from 'vue'
import { ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import { errorMessage } from '@/lib/async'

defineProps({
  /** The names of the passkeys that go with the app. */
  passkeys: { type: Array as PropType<string[]>, default: () => [] },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits<{ done: [] }>()

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
    await api.auth.mfaClear(password.value)
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
    title="Remove the authenticator app"
    description="Passkeys go with it. Sign-in asks for the password alone."
  >
    <form id="remove-app-form" class="space-y-4" @submit.prevent="submit">
      <div v-if="passkeys.length" class="space-y-1 text-sm">
        <p class="font-medium">Passkeys removed</p>
        <ul class="list-disc pl-5">
          <li v-for="name in passkeys" :key="name">{{ name }}</li>
        </ul>
      </div>
      <FormField id="remove-app-password" label="Password">
        <input
          id="remove-app-password"
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
        form="remove-app-form"
        kind="danger"
        label="Remove"
        busy-label="Removing…"
        :busy="busy"
        :disabled="!password"
      />
    </template>
  </AppDialog>
</template>
