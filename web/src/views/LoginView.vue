<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import ActionButton from '@/components/ActionButton.vue'
import AuthCard from '@/components/AuthCard.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
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
const code = ref('')
const error = ref('')
const busy = ref(false)
/** Waiting on the browser's passkey prompt. */
const prompting = ref(false)
const passwordEl = ref(null)
const codeEl = ref(null)

const passkeys = computed(() => Boolean(auth.pending?.passkeys) && webauthn.available())

// The step that opens takes the focus: the code, or the password again.
watch(
  () => auth.pending !== null,
  (second) => {
    if (second) {
      code.value = ''
      codeEl.value?.focus()
    } else passwordEl.value?.focus()
  },
  { flush: 'post' },
)

async function land() {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
  await router.replace(/^\/(?![/\\])/.test(redirect) ? redirect : '/')
}

async function submit() {
  error.value = ''
  busy.value = true
  try {
    if (await auth.login(username.value.trim(), password.value)) await land()
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

/** What the second step says went wrong. */
function secondStepError(e) {
  if (!(e instanceof ApiError)) return e instanceof Error ? e.message : String(e)
  if (e.status === 429) return 'Too many failed attempts. Try again in a few minutes.'
  if (/used already/i.test(e.message)) return 'That code was used already.'
  if (/invalid code/i.test(e.message)) return 'Invalid code.'
  if (/authentication required/i.test(e.message))
    return 'This sign-in has ended. Go back and enter your password again.'
  return e.message
}

async function submitCode() {
  error.value = ''
  busy.value = true
  try {
    await auth.loginTOTP(code.value.replace(/\s+/g, ''))
    await land()
  } catch (e) {
    error.value = secondStepError(e)
    codeEl.value?.focus()
    codeEl.value?.select()
  } finally {
    busy.value = false
  }
}

async function usePasskey() {
  error.value = ''
  prompting.value = true
  try {
    await auth.loginPasskey()
    await land()
  } catch (e) {
    error.value = secondStepError(e)
  } finally {
    prompting.value = false
  }
}

function back() {
  error.value = ''
  auth.cancelPending()
}
</script>

<template>
  <AuthCard title="Sign in">
    <template #subtitle>
      <span v-if="hostname" class="block truncate font-mono">{{ hostname }}</span>
      <!-- Held invisible while the server is asked, so the card does not move. -->
      <span v-else :class="{ invisible: hostname === null }">Ostiole firewall</span>
    </template>
    <form v-if="!auth.pending" class="space-y-4" @submit.prevent="submit">
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
          ref="passwordEl"
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
    <form v-else class="space-y-4" @submit.prevent="submitCode">
      <FormField v-if="auth.pending.totp" id="code" label="Code from your authenticator app">
        <input
          id="code"
          ref="codeEl"
          v-model="code"
          name="code"
          inputmode="numeric"
          autocomplete="one-time-code"
          autocapitalize="none"
          spellcheck="false"
          required
          class="input font-mono"
        />
      </FormField>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
      <ActionButton
        v-if="auth.pending.totp"
        type="submit"
        kind="primary"
        class="w-full"
        label="Sign in"
        busy-label="Signing in…"
        :busy="busy"
        :disabled="prompting"
      />
      <div class="flex gap-2">
        <button
          type="button"
          class="btn-secondary flex-1"
          :disabled="busy || prompting"
          @click="back"
        >
          Back
        </button>
        <ActionButton
          v-if="passkeys"
          class="flex-1"
          label="Use a passkey"
          busy-label="Waiting…"
          :busy="prompting"
          :disabled="busy"
          @click="usePasskey"
        />
      </div>
    </form>
  </AuthCard>
</template>
