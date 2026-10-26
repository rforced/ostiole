<script setup lang="ts">
import { Check, Copy } from '@lucide/vue'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { encode, svgPath } from '@/lib/qr'
import type { TotpStartReply } from '@/lib/types.gen'

defineProps({
  /** The account has an app already, and this one takes its place. */
  replacing: { type: Boolean, default: false },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['done'])

/** The secret the router made and the otpauth address that carries it. */
const setup = ref<TotpStartReply | null>(null)
const code = ref('')
const error = ref('')
const loading = ref(false)
const busy = ref(false)
const secretEl = ref<HTMLInputElement | null>(null)
const codeEl = ref<HTMLInputElement | null>(null)
/** 'copied', 'selected' when only the fallback's selection worked, or ''. */
const copied = ref('')
let copyTimer = 0
let opened = 0
onBeforeUnmount(() => window.clearTimeout(copyTimer))

const digits = computed(() => code.value.replace(/\s+/g, ''))
const valid = computed(() => setup.value !== null && /^\d{6}$/.test(digits.value))

// Black on white in either theme: a scanner reads the code, not the page.
const qr = computed(() => (setup.value ? encode(setup.value.uri) : null))
const qrPath = computed(() => (qr.value ? svgPath(qr.value) : ''))

watch(
  open,
  async (on) => {
    opened += 1
    const mine = opened
    setup.value = null
    code.value = ''
    error.value = ''
    copied.value = ''
    if (!on) return
    loading.value = true
    try {
      const made = await api.auth.totpStart()
      if (mine !== opened) return
      setup.value = made
      await nextTick()
      codeEl.value?.focus()
    } catch (e) {
      if (mine === opened) error.value = errorMessage(e)
    } finally {
      if (mine === opened) loading.value = false
    }
  },
  { immediate: true },
)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.auth.totpConfirm(digits.value)
    emit('done')
    open.value = false
  } catch (e) {
    error.value =
      e instanceof ApiError && /invalid code/i.test(e.message) ? 'Invalid code.' : errorMessage(e)
    codeEl.value?.focus()
    codeEl.value?.select()
  } finally {
    busy.value = false
  }
}

/**
 * The clipboard API needs HTTPS or localhost, which a router reached over
 * plain HTTP on its LAN is not, so that falls back to selecting the secret
 * and the older copy command. Either way the secret ends up selected.
 */
async function copySecret() {
  if (!secretEl.value || !setup.value) return
  secretEl.value.select()
  let ok
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(setup.value.secret)
      ok = true
    } else ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  copied.value = ok ? 'copied' : 'selected'
  window.clearTimeout(copyTimer)
  copyTimer = window.setTimeout(() => (copied.value = ''), 2000)
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :read-only="false"
    :title="replacing ? 'Replace authenticator app' : 'Set up authenticator app'"
    :description="replacing ? 'The current app stops working once you confirm.' : ''"
  >
    <form id="totp-form" class="space-y-4" @submit.prevent="submit">
      <div class="flex flex-wrap items-start gap-4">
        <svg
          v-if="qr"
          role="img"
          aria-label="QR code for the authenticator app"
          :viewBox="`0 0 ${qr.size + 8} ${qr.size + 8}`"
          class="size-56 shrink-0 rounded-md bg-white max-sm:mx-auto"
          shape-rendering="crispEdges"
        >
          <path :d="qrPath" fill="#000" />
        </svg>
        <div
          v-else-if="loading"
          class="skeleton size-56 shrink-0 max-sm:mx-auto"
          aria-hidden="true"
        ></div>
        <div class="min-w-0 flex-1 basis-60 space-y-4">
          <FormField
            id="totp-secret"
            label="Secret"
            hint="Scan the code, or enter the secret by hand in the app, then type the code it shows."
          >
            <div class="flex gap-2">
              <input
                id="totp-secret"
                ref="secretEl"
                :value="setup?.secret ?? ''"
                readonly
                autocomplete="off"
                spellcheck="false"
                class="input font-mono"
                @focus="($event.target as HTMLInputElement).select()"
              />
              <button
                type="button"
                class="btn-secondary shrink-0"
                :disabled="!setup"
                @click="copySecret"
              >
                <Check v-if="copied === 'copied'" class="size-4" aria-hidden="true" />
                <Copy v-else class="size-4" aria-hidden="true" />
                {{ copied === 'copied' ? 'Copied' : copied === 'selected' ? 'Selected' : 'Copy' }}
              </button>
            </div>
          </FormField>
          <FormField id="totp-code" label="Code">
            <input
              id="totp-code"
              ref="codeEl"
              v-model="code"
              inputmode="numeric"
              autocomplete="one-time-code"
              autocapitalize="none"
              spellcheck="false"
              maxlength="8"
              :disabled="!setup"
              required
              class="input w-40 font-mono max-sm:w-full"
            />
          </FormField>
        </div>
      </div>
      <span class="sr-only" aria-live="polite">{{
        copied === 'copied' ? 'Copied.' : copied === 'selected' ? 'Selected, copy it by hand.' : ''
      }}</span>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <ActionButton
        type="submit"
        form="totp-form"
        kind="primary"
        label="Confirm"
        busy-label="Confirming…"
        :busy="busy"
        :disabled="!valid"
      />
    </template>
  </AppDialog>
</template>
