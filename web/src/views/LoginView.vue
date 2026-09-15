<script setup>
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import AuthCard from '@/components/AuthCard.vue'
import FormField from '@/components/FormField.vue'
import { ApiError } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await auth.login(username.value.trim(), password.value)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await router.replace(redirect.startsWith('/') ? redirect : '/')
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) error.value = 'Invalid username or password.'
    else if (e instanceof ApiError && e.status === 429)
      error.value = 'Too many failed attempts. Try again in a few minutes.'
    else error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
    password.value = ''
  }
}
</script>

<template>
  <AuthCard title="Sign in" subtitle="Ostiole firewall">
    <form class="space-y-4" @submit.prevent="submit">
      <FormField id="username" label="Username">
        <input
          id="username"
          v-model="username"
          name="username"
          autocomplete="username"
          autocapitalize="none"
          spellcheck="false"
          required
          class="input"
        />
      </FormField>
      <FormField id="password" label="Password">
        <input
          id="password"
          v-model="password"
          type="password"
          name="password"
          autocomplete="current-password"
          required
          class="input"
        />
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <button type="submit" :disabled="busy" class="btn-primary w-full">
        {{ busy ? 'Signing in…' : 'Sign in' }}
      </button>
    </form>
  </AuthCard>
</template>
