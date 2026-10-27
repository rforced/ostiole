<script setup lang="ts">
import { ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import * as webauthn from '@/lib/webauthn'

defineProps({
  /** The name the passkey is made for: it signs in only there. */
  rpId: { type: String, default: '' },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits<{ done: [] }>()

const name = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

watch(
  open,
  (on) => {
    if (!on) return
    name.value = ''
    password.value = ''
    error.value = ''
  },
  { immediate: true },
)

/** The router's challenge, on the password, then the browser's prompt, then the router's check. */
async function submit() {
  error.value = ''
  busy.value = true
  try {
    const { publicKey } = await api.auth.passkeyOptions(password.value)
    const credential = await webauthn.create(publicKey)
    await api.auth.addPasskey(name.value.trim(), credential)
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
    title="Add passkey"
    :description="rpId ? `Made for ${rpId}. It signs in only there.` : ''"
  >
    <form id="passkey-form" class="space-y-4" @submit.prevent="submit">
      <FormField id="passkey-name" label="Name" hint="Up to 40 characters.">
        <input
          id="passkey-name"
          v-model="name"
          maxlength="40"
          autocomplete="off"
          required
          class="input"
        />
      </FormField>
      <FormField id="passkey-password" label="Password">
        <input
          id="passkey-password"
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          class="input"
        />
      </FormField>
      <p v-if="busy" role="status" class="text-sm text-ink-muted">
        Follow the prompt from your browser or device.
      </p>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <ActionButton
        type="submit"
        form="passkey-form"
        kind="primary"
        label="Add passkey"
        busy-label="Adding…"
        :busy="busy"
        :disabled="!name.trim() || !password"
      />
    </template>
  </AppDialog>
</template>
