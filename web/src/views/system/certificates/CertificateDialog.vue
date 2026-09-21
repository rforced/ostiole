<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The certificate being edited, or null for a new one. */
  certificate: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()

const KEY_TYPES = ['ec256', 'ec384', 'rsa2048', 'rsa4096']
const PROFILES = [
  { value: '', label: "The CA's default" },
  { value: 'classic', label: 'classic' },
  { value: 'tlsserver', label: 'tlsserver' },
  { value: 'shortlived', label: 'shortlived' },
]

const blank = () => ({
  id: '',
  description: '',
  enabled: true,
  source: 'acme',
  names: '',
  interfaceAddresses: [],
  account: config.acmeAccounts[0]?.id ?? '',
  challenge: 'dns-01',
  provider: config.dnsProviders[0]?.id ?? '',
  keyType: 'ec256',
  profile: '',
  certPem: '',
  keyPem: '',
})

const form = ref(blank())
watch(
  () => [open.value, props.certificate],
  () => {
    if (!open.value) return
    const c = props.certificate
    form.value = c
      ? {
          ...blank(),
          ...c,
          names: (c.names ?? []).join('\n'),
          interfaceAddresses: [...(c.interfaceAddresses ?? [])],
          keyType: c.keyType || 'ec256',
          profile: c.profile ?? '',
        }
      : blank()
  },
  { immediate: true },
)

/** The interfaces a CA could reach: the ones in an external zone. */
const external = computed(() => {
  const zones = new Set(config.zones.filter((z) => z.external).map((z) => z.name))
  return config.interfaces.filter((i) => zones.has(i.zone))
})

const names = computed(() => parseList(form.value.names))
const hasWildcard = computed(() => names.value.some((n) => n.startsWith('*.')))
const hasAddress = computed(
  () => names.value.some(isAddress) || form.value.interfaceAddresses.length > 0,
)

/** A literal address rather than a name; a CA only issues those over http-01. */
function isAddress(name) {
  return /^[0-9.]+$/.test(name) || name.includes(':')
}

// An address is checked over http-01 and a wildcard over dns-01; neither
// is a choice, so the field follows rather than waiting to be refused.
watch([hasWildcard, hasAddress], ([wildcard, address]) => {
  if (wildcard) form.value.challenge = 'dns-01'
  else if (address) form.value.challenge = 'http-01'
})
watch(hasAddress, (address) => {
  if (address) form.value.profile = 'shortlived'
})

const valid = computed(() => {
  if (!form.value.id) return false
  if (form.value.source === 'uploaded') return Boolean(form.value.certPem && form.value.keyPem)
  if (!names.value.length && !form.value.interfaceAddresses.length) return false
  if (!form.value.account) return false
  return form.value.challenge !== 'dns-01' || Boolean(form.value.provider)
})

function save() {
  const f = form.value
  const out = { id: f.id.trim(), enabled: f.enabled, source: f.source }
  if (f.description) out.description = f.description
  if (f.source === 'uploaded') {
    out.certPem = f.certPem
    out.keyPem = f.keyPem
  } else {
    if (names.value.length) out.names = names.value
    if (f.interfaceAddresses.length) out.interfaceAddresses = [...f.interfaceAddresses]
    out.account = f.account
    out.challenge = f.challenge
    if (f.challenge === 'dns-01') out.provider = f.provider
    if (f.keyType && f.keyType !== 'ec256') out.keyType = f.keyType
    if (f.profile) out.profile = f.profile
  }
  config.upsertCertificate(out, props.certificate?.id)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="certificate ? `Certificate ${certificate.id}` : 'Add certificate'"
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          id="cert-id"
          label="Name"
          hint="Used in the file path and the API. Cannot change later."
        >
          <input
            id="cert-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
            :disabled="Boolean(certificate)"
          />
        </FormField>
        <FormField id="cert-desc" label="Description">
          <input id="cert-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded" />
        Enabled
      </label>

      <FormField id="cert-source" label="Source">
        <select
          id="cert-source"
          v-model="form.source"
          class="input"
          :disabled="Boolean(certificate)"
        >
          <option value="acme">Issued by a CA</option>
          <option value="uploaded">Uploaded</option>
        </select>
      </FormField>

      <template v-if="form.source === 'acme'">
        <FormField
          id="cert-names"
          label="Names"
          hint="One per line. Names, wildcards or public addresses."
        >
          <textarea
            id="cert-names"
            v-model="form.names"
            class="input h-24 font-mono"
            spellcheck="false"
            placeholder="router.example.com"
          />
        </FormField>

        <FormField
          id="cert-ifaces"
          label="Interfaces"
          hint="Their public addresses, as they are at issuance."
        >
          <div id="cert-ifaces" class="flex flex-wrap gap-3">
            <label v-for="i in external" :key="i.name" class="flex items-center gap-2 text-sm">
              <input
                v-model="form.interfaceAddresses"
                type="checkbox"
                class="size-4 rounded"
                :value="i.name"
              />
              <span class="font-mono text-code">{{ i.name }}</span>
            </label>
            <span v-if="!external.length" class="text-sm text-ink-muted">
              No interface is in an external zone.
            </span>
          </div>
        </FormField>

        <div class="grid gap-4 sm:grid-cols-2">
          <FormField
            id="cert-challenge"
            label="Challenge"
            :hint="
              form.challenge === 'http-01'
                ? 'Port 80 opens on every external zone.'
                : 'Needs a DNS provider.'
            "
          >
            <select
              id="cert-challenge"
              v-model="form.challenge"
              class="input"
              :disabled="hasWildcard || hasAddress"
            >
              <option value="dns-01">dns-01</option>
              <option value="http-01">http-01</option>
            </select>
          </FormField>
          <FormField id="cert-account" label="Account">
            <select id="cert-account" v-model="form.account" class="input" required>
              <option v-for="a in config.acmeAccounts" :key="a.id" :value="a.id">{{ a.id }}</option>
            </select>
          </FormField>
        </div>

        <FormField v-if="form.challenge === 'dns-01'" id="cert-provider" label="DNS provider">
          <select id="cert-provider" v-model="form.provider" class="input" required>
            <option v-for="p in config.dnsProviders" :key="p.id" :value="p.id">
              {{ p.id }} ({{ p.kind }})
            </option>
          </select>
        </FormField>

        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="cert-keytype" label="Key type">
            <select id="cert-keytype" v-model="form.keyType" class="input">
              <option v-for="k in KEY_TYPES" :key="k" :value="k">{{ k }}</option>
            </select>
          </FormField>
          <FormField
            id="cert-profile"
            label="Profile"
            hint="What the CA offers. Addresses need shortlived, six days."
          >
            <select id="cert-profile" v-model="form.profile" class="input" :disabled="hasAddress">
              <option v-for="p in PROFILES" :key="p.value" :value="p.value">{{ p.label }}</option>
            </select>
          </FormField>
        </div>
      </template>

      <template v-else>
        <FormField
          id="cert-pem"
          label="Certificate (PEM)"
          hint="Intermediates go under it, in the same field."
        >
          <textarea
            id="cert-pem"
            v-model="form.certPem"
            class="input h-32 font-mono"
            placeholder="-----BEGIN CERTIFICATE-----"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField id="cert-key" label="Private key (PEM)">
          <textarea
            id="cert-key"
            v-model="form.keyPem"
            class="input h-32 font-mono"
            placeholder="-----BEGIN PRIVATE KEY-----"
            spellcheck="false"
            required
          />
        </FormField>
      </template>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!valid">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
