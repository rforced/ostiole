<script setup>
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'

import AuthCard from '@/components/AuthCard.vue'
import FormField from '@/components/FormField.vue'
import { ApiError } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'

const MIN_PASSWORD = 12
/** The same rule as the input's pattern, so the button waits for it too. */
const USERNAME = /^[a-z][a-z0-9_.-]{0,31}$/

const auth = useAuthStore()
const router = useRouter()

const username = ref('admin')
const password = ref('')
const confirm = ref('')
const error = ref('')
const busy = ref(false)

const badName = computed(() => username.value !== '' && !USERNAME.test(username.value.trim()))
const mismatch = computed(() => confirm.value !== '' && confirm.value !== password.value)
const tooShort = computed(() => password.value !== '' && password.value.length < MIN_PASSWORD)
const valid = computed(
  () =>
    USERNAME.test(username.value.trim()) &&
    password.value.length >= MIN_PASSWORD &&
    confirm.value === password.value,
)

async function submit() {
  if (!valid.value) return
  error.value = ''
  busy.value = true
  try {
    await auth.setup(username.value.trim(), password.value)
    await router.replace('/')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthCard title="Create the admin account" subtitle="This router has no accounts yet.">
    <form class="space-y-4" @submit.prevent="submit">
      <FormField
        id="username"
        label="Username"
        hint="Lowercase letters, digits, '_', '.', or '-'. Starts with a letter."
      >
        <input
          id="username"
          v-model="username"
          name="username"
          autocomplete="username"
          autocapitalize="none"
          spellcheck="false"
          pattern="[a-z][a-z0-9_.\-]{0,31}"
          required
          class="input"
          :aria-invalid="badName"
        />
      </FormField>
      <FormField id="password" label="Password" :hint="`At least ${MIN_PASSWORD} characters.`">
        <input
          id="password"
          v-model="password"
          type="password"
          name="new-password"
          autocomplete="new-password"
          :minlength="MIN_PASSWORD"
          required
          class="input"
          :aria-invalid="tooShort"
        />
      </FormField>
      <FormField id="confirm" label="Repeat password">
        <input
          id="confirm"
          v-model="confirm"
          type="password"
          name="confirm-password"
          autocomplete="new-password"
          required
          class="input"
          :aria-invalid="mismatch"
        />
        <p v-if="mismatch" class="text-sm text-bad">Passwords do not match.</p>
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-bad">{{ error }}</p>
      <button type="submit" :disabled="busy || !valid" class="btn-primary w-full">
        {{ busy ? 'Creating…' : 'Create account and sign in' }}
      </button>
    </form>
  </AuthCard>
</template>
