<script setup>
import { RefreshCw, Upload } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfirmStore } from '@/stores/confirm'
import { useToastStore } from '@/stores/toast'

const confirm = useConfirmStore()
const toast = useToastStore()
const cert = ref(null)
const uploading = ref(false)
const form = ref({ certificate: '', key: '' })

const load = useAsync(async () => {
  try {
    cert.value = await api.certificate.get()
  } catch (e) {
    // A server without TLS has no certificate to manage, which is the
    // normal state during development.
    cert.value = null
    if (!(e instanceof ApiError && (e.status === 503 || e.status === 404))) throw e
  }
})
onMounted(load.run)

const state = computed(() => {
  if (!cert.value) return null
  if (cert.value.expired) return { tone: 'badge-warn', label: 'expired' }
  if (cert.value.expiresSoon) return { tone: 'badge-warn', label: 'expires soon' }
  if (cert.value.selfSigned) return { tone: '', label: 'self-signed' }
  return { tone: 'badge-ok', label: 'issued' }
})

/** Names the router answers to that the certificate does not cover. */
const missing = computed(() => {
  if (!cert.value) return []
  const covered = new Set(cert.value.names ?? [])
  return (cert.value.hosts ?? []).filter((h) => h && !covered.has(h))
})

const regenerate = useAsync(async () => {
  cert.value = await api.certificate.regenerate()
  toast.show('New self-signed certificate in place. Browsers warn about it once more.')
})

const install = useAsync(async () => {
  cert.value = await api.certificate.install(form.value.certificate, form.value.key)
  form.value = { certificate: '', key: '' }
  uploading.value = false
  toast.show('The new certificate is served from the next connection.')
})

const busy = computed(() => regenerate.busy.value || install.busy.value)
const error = computed(() => load.error.value || regenerate.error.value || install.error.value)

async function askRegenerate() {
  const ok = await confirm.ask({
    question: 'Regenerate the self-signed certificate?',
    description: 'Browsers warn about the new one, and its fingerprint is the one to check.',
    confirmLabel: 'Regenerate',
    typed: 'regenerate',
  })
  if (ok) await regenerate.run()
}

async function askInstall() {
  const ok = await confirm.ask({
    question: 'Install this certificate?',
    description: 'It replaces the one being served.',
    confirmLabel: 'Install',
  })
  if (ok) await install.run()
}
</script>

<template>
  <section class="card space-y-3" aria-labelledby="cert-title">
    <h2 id="cert-title" class="card-title">Certificate</h2>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

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
        <dd class="font-mono text-code break-all">{{ cert.fingerprint }}</dd>
        <dt class="text-neutral-500">Key</dt>
        <dd class="font-mono text-code">
          {{ cert.algorithm
          }}<span v-if="cert.chain > 1"> · {{ cert.chain - 1 }} intermediate(s)</span>
        </dd>
      </dl>

      <p v-if="missing.length" role="note" class="text-sm text-amber-700 dark:text-amber-300">
        This router also answers to
        <span class="font-mono">{{ missing.join(', ') }}</span
        >, which the certificate does not cover, so a browser reaching it that way warns.
        Regenerating the self-signed certificate covers every current address.
      </p>

      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn-secondary" :disabled="busy" @click="askRegenerate">
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
        @submit.prevent="askInstall"
      >
        <p class="text-sm text-neutral-500">
          Intermediates go under the certificate, in the same field.
        </p>
        <FormField id="cert-pem" label="Certificate (PEM)">
          <textarea
            id="cert-pem"
            v-model="form.certificate"
            class="input h-32 font-mono"
            placeholder="-----BEGIN CERTIFICATE-----"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField id="cert-key" label="Private key (PEM)">
          <textarea
            id="cert-key"
            v-model="form.key"
            class="input h-32 font-mono"
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
