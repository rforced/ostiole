<script setup lang="ts">
import { type PropType, ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { api, type Passkey } from '@/lib/api'
import { errorMessage } from '@/lib/async'

const props = defineProps({
  /** The passkey being renamed. */
  passkey: { type: Object as PropType<Passkey | null>, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits<{ done: [] }>()

const name = ref('')
const error = ref('')
const busy = ref(false)

watch(
  open,
  (on) => {
    if (!on) return
    name.value = props.passkey?.name ?? ''
    error.value = ''
  },
  { immediate: true },
)

async function submit() {
  const passkey = props.passkey
  if (!passkey) return
  error.value = ''
  busy.value = true
  try {
    await api.auth.renamePasskey(passkey.id, name.value.trim())
    emit('done')
    open.value = false
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppDialog v-model:open="open" :read-only="false" :title="`Rename ${passkey?.name ?? ''}`">
    <form id="passkey-rename-form" class="space-y-4" @submit.prevent="submit">
      <FormField id="passkey-rename-name" label="Name" hint="Up to 40 characters.">
        <input
          id="passkey-rename-name"
          v-model="name"
          maxlength="40"
          autocomplete="off"
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
        form="passkey-rename-form"
        kind="primary"
        label="Rename"
        busy-label="Renaming…"
        :busy="busy"
        :disabled="!name.trim()"
      />
    </template>
  </AppDialog>
</template>
