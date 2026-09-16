<script setup>
import { RefreshCw, Upload } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'

const cert = ref(null)
const error = ref('')
const notice = ref('')
const busy = ref(false)
const uploading = ref(false)
const form = ref({ certificate: '', key: '' })

onMounted(refresh)

async function refresh() {
  error.value = ''
  try {
    cert.value = await api.certificate.get()
  } catch (e) {
    // A server without TLS has no certificate to manage, which is the
    // normal state during development.
    cert.value = null
    if (!(e instanceof ApiError && (e.status === 503 || e.status === 404))) {
      error.value = e instanceof Error ? e.message : String(e)
    }
  }
}

const state = computed(() => {
  if (!cert.value) return null
  if (cert.value.expired) return { tone: 'badge-warn', label: 'expired' }
  if (cert.value.expiresSoon) return { tone: 'badge-warn', label: 'expires soon' }
  if (cert.value.selfSigned) return { tone: '', label: 'self-signed' }
  return { tone: 'badge-ok', label: 'issued' }
})

/** Names the box answers to that the certificate does not cover. */
const missing = computed(() => {
  if (!cert.value) return []
  const covered = new Set(cert.value.names ?? [])
  return (cert.value.hosts ?? []).filter((h) => h && !covered.has(h))
})

async function regenerate() {
  error.value = ''
  notice.value = ''
  busy.value = true
  try {
    cert.value = await api.certificate.regenerate()
    notice.value =
      'A new self-signed certificate is in place. Your browser will warn about it once more, and the fingerprint below is the one to check.'
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function upload() {
  error.value = ''
  notice.value = ''
  busy.value = true
  try {
    cert.value = await api.certificate.install(form.value.certificate, form.value.key)
    form.value = { certificate: '', key: '' }
    uploading.value = false
    notice.value = 'The new certificate is being served from the next connection.'
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="card space-y-3" aria-labelledby="cert-title">
    <h2 id="cert-title" class="card-title">Certificate</h2>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-amber-700 dark:text-amber-300">
      {{ notice }}
    </p>

    <p v-if="!cert" class="text-sm text-neutral-500">
      This server is not serving HTTPS, so there is no certificate to manage.
    </p>
    <template v-else>
      <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
        <dt class="text-neutral-500">Valid for</dt>
        <dd class="font-mono">{{ cert.names.join(', ') || '—' }}</dd>
        <dt class="text-neutral-500">Issued by</dt>
        <dd>
          {{ cert.issuer }}
          <span class="badge ml-1" :class="state.tone">{{ state.label }}</span>
        </dd>
        <dt class="text-neutral-500">Expires</dt>
        <dd>{{ new Date(cert.notAfter).toLocaleString() }}</dd>
        <dt class="text-neutral-500">Fingerprint</dt>
        <dd class="font-mono text-xs break-all">{{ cert.fingerprint }}</dd>
        <dt class="text-neutral-500">Key</dt>
        <dd class="font-mono text-xs">
          {{ cert.algorithm
          }}<span v-if="cert.chain > 1"> · {{ cert.chain - 1 }} intermediate(s)</span>
        </dd>
      </dl>

      <p v-if="missing.length" role="note" class="text-sm text-amber-700 dark:text-amber-300">
        This box also answers to
        <span class="font-mono">{{ missing.join(', ') }}</span
        >, which the certificate does not cover. A browser reaching it that way will warn. Replacing
        the self-signed certificate below covers every current address.
      </p>

      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn-secondary" :disabled="busy" @click="regenerate">
          <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Regenerate self-signed
        </button>
        <button
          type="button"
          class="btn-secondary"
          :disabled="busy"
          @click="uploading = !uploading"
        >
          <Upload class="mr-1 size-4" aria-hidden="true" /> Use my own certificate
        </button>
      </div>

      <form
        v-if="uploading"
        class="space-y-3 border-t pt-3 dark:border-neutral-800"
        @submit.prevent="upload"
      >
        <p class="text-sm text-neutral-500">
          Paste the certificate, with any intermediates below it, and the private key that goes with
          it. Both are checked before anything is replaced, so a mismatch leaves the box reachable.
        </p>
        <FormField id="cert-pem" label="Certificate (PEM)">
          <textarea
            id="cert-pem"
            v-model="form.certificate"
            class="input h-32 font-mono text-xs"
            placeholder="-----BEGIN CERTIFICATE-----"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField id="cert-key" label="Private key (PEM)">
          <textarea
            id="cert-key"
            v-model="form.key"
            class="input h-32 font-mono text-xs"
            placeholder="-----BEGIN PRIVATE KEY-----"
            spellcheck="false"
            required
          />
        </FormField>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn-secondary" @click="uploading = false">Cancel</button>
          <button type="submit" class="btn-primary" :disabled="busy">Install certificate</button>
        </div>
      </form>
    </template>
  </section>
</template>
