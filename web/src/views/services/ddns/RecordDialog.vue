<script setup>
import { computed, ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { newId } from '@/lib/ids'
import { interfaceLabel } from '@/lib/interfaces'
import { providerFor } from '@/lib/providers'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The record being edited, or null to add one. */
  record: { type: Object, default: null },
  /** The provider kinds that can keep a record, from the status. */
  kinds: { type: Array, default: () => [] },
})
const open = defineModel('open', { type: Boolean, default: false })
const auth = useAuthStore()
const config = useConfigStore()
const error = ref('')

/** The interface a new record starts on: the first in an external zone. */
function wan() {
  const external = new Set((config.draft?.zones ?? []).filter((z) => z.external).map((z) => z.name))
  return (config.interfaces.find((i) => external.has(i.zone)) ?? config.interfaces[0])?.name ?? ''
}

const blank = () => ({
  name: '',
  description: '',
  interface: wan(),
  ipv4: true,
  ipv6: false,
  enabled: true,
})
const form = ref(blank())
/** What Check found, one result per record type. */
const checked = ref(null)
const checking = ref(false)
const checkError = ref('')

watch(
  () => [open.value, props.record],
  () => {
    if (!open.value) return
    error.value = ''
    checked.value = null
    checkError.value = ''
    const r = props.record
    form.value = r ? { ...blank(), ...r, ipv4: Boolean(r.ipv4), ipv6: Boolean(r.ipv6) } : blank()
  },
  { immediate: true },
)

/** The name as it is stored: no trailing dot, lower case. */
const name = computed(() => form.value.name.trim().replace(/\.$/, '').toLowerCase())
const found = computed(() => providerFor(config.dnsProviders, name.value))
const kind = computed(() => props.kinds.find((k) => k.kind === found.value?.provider.kind))
/** Where the record is written, in the provider's own name. */
const where = computed(() => kind.value?.label ?? 'The provider')

const providerLine = computed(() => {
  if (!name.value) return 'Under a domain a DNS provider holds.'
  if (!found.value) return `No DNS provider holds ${name.value}.`
  if (props.kinds.length && !kind.value) {
    return `${found.value.provider.id} holds ${found.value.zone}, and cannot keep this record.`
  }
  return `Written through ${found.value.provider.id}${kind.value ? ` (${kind.value.label})` : ''}.`
})

/** The record the form describes, as the draft keeps it. */
function built() {
  const f = form.value
  const out = { id: props.record?.id || newId('ddns'), enabled: f.enabled, name: name.value }
  const description = f.description.trim()
  if (description) out.description = description
  out.interface = f.interface
  if (f.ipv4) out.ipv4 = true
  if (f.ipv6) out.ipv6 = true
  return out
}

/** Why the record cannot be saved, or empty. The server checks it again. */
function problem() {
  const f = form.value
  if (!name.value) return 'A name is needed.'
  if (!f.ipv4 && !f.ipv6) return 'Keep the A record, the AAAA record or both.'
  if (!found.value) {
    return `No DNS provider holds ${name.value}. Add its domain to one under System › DNS providers.`
  }
  if (props.kinds.length && !kind.value) {
    return `${found.value.provider.id} cannot keep a dynamic DNS record.`
  }
  for (const other of config.ddnsRecords) {
    if (other.id === props.record?.id || other.name.toLowerCase() !== name.value) continue
    if (f.ipv4 && other.ipv4) return `Another record keeps the A record for ${name.value}.`
    if (f.ipv6 && other.ipv6) return `Another record keeps the AAAA record for ${name.value}.`
  }
  return ''
}

function save() {
  error.value = problem()
  if (error.value) return
  config.upsertDdnsRecord(built(), props.record?.id)
  open.value = false
}

async function check() {
  checked.value = null
  checkError.value = problem()
  if (checkError.value) return
  checking.value = true
  try {
    checked.value = await api.ddns.check(config.draft, built())
  } catch (e) {
    checkError.value = errorMessage(e)
  } finally {
    checking.value = false
  }
}

const FAMILY = { A: 'IPv4', AAAA: 'IPv6' }

/** What the check found for one record type, as a sentence. */
function sentence(r) {
  if (r.error) return r.error
  const holds = (r.published ?? []).join(', ')
  switch (r.action) {
    case 'create':
      return `${where.value} has no ${r.type} record. The apply creates it with ${r.address}.`
    case 'change':
      return `${where.value} holds ${holds}. The apply changes it to ${r.address}.`
    case 'none':
      return `${where.value} holds ${r.address} already.`
    case 'no-address':
      return `${form.value.interface} has no public ${FAMILY[r.type]} address, so ${where.value} keeps ${holds || 'nothing'}.`
    case 'several':
      return `${where.value} holds ${r.published.length} ${r.type} records for this name. Keep one there first.`
  }
  return r.action
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="record ? `Record ${record.name}` : 'Add record'"
    description="Anyone can look this name up."
  >
    <form id="ddns-record-form" class="space-y-4" @submit.prevent="save">
      <ToggleRow v-model="form.enabled" label="Enabled" />
      <div class="fields">
        <FormField id="ddns-name" label="Name" :hint="providerLine">
          <input
            id="ddns-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
            autocomplete="off"
          />
        </FormField>
        <FormField id="ddns-if" label="Interface" hint="Its public address is published.">
          <select id="ddns-if" v-model="form.interface" class="input" required>
            <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
              {{ interfaceLabel(i) }}{{ i.enabled ? '' : ', off' }}
            </option>
          </select>
        </FormField>
        <FormField id="ddns-desc" label="Description">
          <input id="ddns-desc" v-model="form.description" class="input" />
        </FormField>
      </div>
      <div class="space-y-2">
        <ToggleRow v-model="form.ipv4" label="A record" hint="The IPv4 address." />
        <ToggleRow v-model="form.ipv6" label="AAAA record" hint="The IPv6 address." />
      </div>

      <div v-if="checked" role="status">
        <ul class="space-y-1 text-sm">
          <li v-for="r in checked" :key="r.type" :class="{ 'text-bad': r.error }">
            <span class="font-mono">{{ r.type }}:</span>&nbsp;{{ sentence(r) }}
          </li>
        </ul>
      </div>
      <ErrorLine v-if="checkError" class="text-sm">{{ checkError }}</ErrorLine>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <ActionButton
        v-if="!auth.readOnly"
        class="mr-auto"
        label="Check"
        busy-label="Checking…"
        :busy="checking"
        @click="check"
      />
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="ddns-record-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
