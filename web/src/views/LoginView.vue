<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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

const TOO_MANY = 'Too many failed attempts. Try again in a few minutes.'

const username = ref('')
const password = ref('')
const code = ref('')
const error = ref('')
const busy = ref(false)
/** Waiting on the browser's passkey prompt. */
const prompting = ref(false)
/** The username was given and the password is asked. */
const entered = ref(false)
/** What the first step said about a passkey alone: {token, publicKey} or null. */
const offer = ref(null)
const usernameEl = ref(null)
const passwordEl = ref(null)
const codeEl = ref(null)
/** Closes a passkey prompt still open. */
let prompt = null

const step = computed(() => (auth.pending ? 3 : entered.value ? 2 : 1))
const passkeys = computed(() => Boolean(auth.pending?.passkeys) && webauthn.available())
const passkeyAlone = computed(() => offer.value !== null && webauthn.available())

/** A prompt the user closed or the page aborted: nothing to say. */
const cancelled = (e) => e?.name === 'NotAllowedError' || e?.name === 'AbortError'

// The step that opens takes the focus.
watch(
  step,
  (s) => {
    if (s === 3) {
      code.value = ''
      codeEl.value?.focus()
    } else if (s === 2) passwordEl.value?.focus()
    else usernameEl.value?.focus()
  },
  { flush: 'post' },
)

onBeforeUnmount(() => prompt?.abort())

async function land() {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
  await router.replace(/^\/(?![/\\])/.test(redirect) ? redirect : '/')
}

async function next() {
  error.value = ''
  const name = username.value.trim()
  busy.value = true
  try {
    offer.value = await auth.start(name)
  } catch (e) {
    if (e instanceof ApiError && e.status === 429) error.value = TOO_MANY
    else error.value = e instanceof Error ? e.message : String(e)
    return
  } finally {
    busy.value = false
  }
  username.value = name
  entered.value = true
  if (passkeyAlone.value) signInWithPasskey()
}

function change() {
  prompt?.abort()
  error.value = ''
  password.value = ''
  offer.value = null
  entered.value = false
}

async function submit() {
  prompt?.abort()
  error.value = ''
  busy.value = true
  try {
    if (await auth.login(username.value, password.value)) await land()
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) error.value = 'Invalid username or password.'
    else if (e instanceof ApiError && e.status === 429) error.value = TOO_MANY
    else error.value = e instanceof Error ? e.message : String(e)
    passwordEl.value?.focus()
  } finally {
    busy.value = false
    password.value = ''
  }
}

/** Signs in with the passkey the first step offered, no password. */
async function signInWithPasskey() {
  prompt?.abort()
  const ctl = new AbortController()
  prompt = ctl
  error.value = ''
  prompting.value = true
  try {
    await auth.loginWithPasskey(offer.value.token, offer.value.publicKey, ctl.signal)
    await land()
  } catch (e) {
    if (ctl.signal.aborted || cancelled(e)) return
    error.value = passkeyError(e)
    // The router spent that token; the next try needs a fresh one.
    offer.value = await auth.start(username.value).catch(() => null)
  } finally {
    if (prompt === ctl) {
      prompt = null
      prompting.value = false
    }
  }
}

/** Why a passkey alone did not sign in, with the password still there to type. */
function passkeyError(e) {
  if (!(e instanceof ApiError)) return e instanceof Error ? e.message : String(e)
  if (e.status === 429) return TOO_MANY
  if (/does not sign in with a passkey/i.test(e.message))
    return 'Passkey sign-in is off for this account. Enter your password.'
  if (e.status === 401) return 'The passkey was refused. Enter your password.'
  return e.message
}

/** What the second step says went wrong. */
function secondStepError(e) {
  if (!(e instanceof ApiError)) return e instanceof Error ? e.message : String(e)
  if (e.status === 429) return TOO_MANY
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
    <form v-if="step === 1" class="space-y-4" @submit.prevent="next">
      <FormField id="username" label="Username">
        <input
          id="username"
          ref="usernameEl"
          v-model="username"
          name="username"
          autocomplete="username"
          autocapitalize="none"
          spellcheck="false"
          required
          class="input"
        />
      </FormField>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
      <ActionButton
        type="submit"
        kind="primary"
        class="w-full"
        label="Continue"
        busy-label="Continuing…"
        :busy="busy"
      />
    </form>
    <form v-else-if="step === 2" class="space-y-4" @submit.prevent="submit">
      <input hidden name="username" autocomplete="username" :value="username" readonly />
      <div class="flex items-center justify-between gap-2 text-sm">
        <span class="truncate font-medium">{{ username }}</span>
        <button type="button" class="link-action" :disabled="busy" @click="change">Change</button>
      </div>
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
      <div class="flex gap-2">
        <ActionButton
          type="submit"
          kind="primary"
          class="flex-1"
          label="Sign in"
          busy-label="Signing in…"
          :busy="busy"
        />
        <ActionButton
          v-if="passkeyAlone"
          class="flex-1"
          label="Use a passkey"
          busy-label="Waiting…"
          :busy="prompting"
          :disabled="busy"
          @click="signInWithPasskey"
        />
      </div>
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
