<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The account being edited, or null for a new one. */
  account: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()

const LETS_ENCRYPT = 'https://acme-v02.api.letsencrypt.org/directory'
const LETS_ENCRYPT_STAGING = 'https://acme-staging-v02.api.letsencrypt.org/directory'
const CAS = [
  { value: LETS_ENCRYPT, label: "Let's Encrypt" },
  { value: LETS_ENCRYPT_STAGING, label: "Let's Encrypt (staging)" },
  { value: 'custom', label: 'Custom' },
]

const blank = () => ({
  id: '',
  description: '',
  ca: LETS_ENCRYPT,
  directory: LETS_ENCRYPT,
  email: '',
  eabKeyId: '',
  eabHmac: '',
  privateKey: '',
  caCert: '',
})

const form = ref(blank())
const showEAB = ref(false)
const showCACert = ref(false)

/** A new account gets a key from the router; it is never shown or pasted. */
const key = useAsync(async () => {
  const { privateKey } = await api.certificates.key()
  form.value.privateKey = privateKey
})

watch(
  () => [open.value, props.account],
  () => {
    if (!open.value) return
    const a = props.account
    if (a) {
      const known = a.directory === LETS_ENCRYPT || a.directory === LETS_ENCRYPT_STAGING
      form.value = { ...blank(), ...a, ca: known ? a.directory : 'custom' }
      showEAB.value = Boolean(a.eabKeyId)
      showCACert.value = Boolean(a.caCert)
      return
    }
    form.value = blank()
    showEAB.value = false
    showCACert.value = false
    key.run()
  },
  { immediate: true },
)

watch(
  () => form.value.ca,
  (ca) => {
    if (ca !== 'custom') form.value.directory = ca
    else if (form.value.directory === LETS_ENCRYPT || form.value.directory === LETS_ENCRYPT_STAGING)
      form.value.directory = ''
  },
)

const valid = computed(
  () => Boolean(form.value.id) && Boolean(form.value.directory) && Boolean(form.value.privateKey),
)

function save() {
  const f = form.value
  const out = { id: f.id.trim(), directory: f.directory.trim(), privateKey: f.privateKey }
  if (f.description) out.description = f.description
  if (f.email) out.email = f.email.trim()
  if (showEAB.value && f.eabKeyId) {
    out.eabKeyId = f.eabKeyId.trim()
    out.eabHmac = f.eabHmac.trim()
  }
  if (showCACert.value && f.caCert) out.caCert = f.caCert
  config.upsertAcmeAccount(out, props.account?.id)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="account ? `Account ${account.id}` : 'Add ACME account'">
    <form class="space-y-4" @submit.prevent="save">
      <p v-if="key.error.value" role="alert" class="text-sm text-bad">
        {{ key.error.value }}
      </p>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="acc-id" label="Name">
          <input
            id="acc-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
            :disabled="Boolean(account)"
          />
        </FormField>
        <FormField id="acc-desc" label="Description">
          <input id="acc-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <FormField id="acc-ca" label="CA">
        <select id="acc-ca" v-model="form.ca" class="input">
          <option v-for="c in CAS" :key="c.value" :value="c.value">{{ c.label }}</option>
        </select>
      </FormField>

      <FormField v-if="form.ca === 'custom'" id="acc-directory" label="Directory URL">
        <input
          id="acc-directory"
          v-model="form.directory"
          class="input font-mono"
          required
          spellcheck="false"
          placeholder="https://acme.example.com/directory"
        />
      </FormField>

      <FormField id="acc-email" label="Email" hint="Optional. The CA writes about expiry.">
        <input id="acc-email" v-model="form.email" type="email" class="input" />
      </FormField>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="showEAB" type="checkbox" class="size-4 rounded" />
        This CA gave me account credentials
      </label>
      <div v-if="showEAB" class="grid gap-4 sm:grid-cols-2">
        <FormField id="acc-eab-kid" label="EAB key ID">
          <input
            id="acc-eab-kid"
            v-model="form.eabKeyId"
            class="input font-mono"
            spellcheck="false"
          />
        </FormField>
        <FormField id="acc-eab-hmac" label="EAB HMAC">
          <input
            id="acc-eab-hmac"
            v-model="form.eabHmac"
            type="password"
            class="input font-mono"
            autocomplete="off"
          />
        </FormField>
      </div>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="showCACert" type="checkbox" class="size-4 rounded" />
        This CA has its own root
      </label>
      <FormField v-if="showCACert" id="acc-cacert" label="CA certificate (PEM)">
        <textarea
          id="acc-cacert"
          v-model="form.caCert"
          class="input h-24 font-mono"
          placeholder="-----BEGIN CERTIFICATE-----"
          spellcheck="false"
        />
      </FormField>

      <p class="text-sm text-ink-muted">Saving agrees to the CA's terms.</p>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!valid || key.busy.value">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
