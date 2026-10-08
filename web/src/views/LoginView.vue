<script setup>
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import ActionButton from '@/components/ActionButton.vue'
import AuthCard from '@/components/AuthCard.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

/** The router's hostname: '' when it has none, null until the server says. */
const hostname = ref(null)
onMounted(async () => {
  try {
    hostname.value = (await api.auth.loginPage()).hostname ?? ''
  } catch {
    hostname.value = ''
  }
})

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
    await router.replace(/^\/(?![/\\])/.test(redirect) ? redirect : '/')
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
  <AuthCard title="Sign in">
    <template #subtitle>
      <span v-if="hostname" class="block truncate font-mono">{{ hostname }}</span>
      <!-- Held invisible while the server is asked, so the card does not move. -->
      <span v-else :class="{ invisible: hostname === null }">Ostiole firewall</span>
    </template>
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
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
      <ActionButton
        type="submit"
        kind="primary"
        class="w-full"
        label="Sign in"
        busy-label="Signing in…"
        :busy="busy"
      />
    </form>
  </AuthCard>
</template>
