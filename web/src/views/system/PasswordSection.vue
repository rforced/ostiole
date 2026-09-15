<script setup>
import { computed, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'

const MIN = 12
const current = ref('')
const next = ref('')
const repeat = ref('')
const error = ref('')
const done = ref(false)
const busy = ref(false)

const valid = computed(
  () => current.value !== '' && next.value.length >= MIN && next.value === repeat.value,
)

async function submit() {
  error.value = ''
  done.value = false
  busy.value = true
  try {
    await api.auth.changePassword(current.value, next.value)
    done.value = true
    current.value = next.value = repeat.value = ''
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) error.value = 'Current password is wrong.'
    else error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="card space-y-4" aria-labelledby="pw-title">
    <h2 id="pw-title" class="card-title">Change password</h2>
    <form class="max-w-sm space-y-3" @submit.prevent="submit">
      <FormField id="pw-current" label="Current password">
        <input
          id="pw-current"
          v-model="current"
          type="password"
          autocomplete="current-password"
          class="input"
          required
        />
      </FormField>
      <FormField id="pw-new" label="New password" :hint="`At least ${MIN} characters.`">
        <input
          id="pw-new"
          v-model="next"
          type="password"
          autocomplete="new-password"
          class="input"
          :minlength="MIN"
          required
        />
      </FormField>
      <FormField id="pw-repeat" label="Repeat new password">
        <input
          id="pw-repeat"
          v-model="repeat"
          type="password"
          autocomplete="new-password"
          class="input"
          required
        />
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <p v-if="done" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">
        Password changed.
      </p>
      <button type="submit" class="btn-primary" :disabled="busy || !valid">Change password</button>
    </form>
  </section>
</template>
