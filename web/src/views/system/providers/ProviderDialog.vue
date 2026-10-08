<script setup>
import { computed, ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { joinList, parseList } from '@/lib/lists'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The provider being edited, or null for a new one. */
  provider: { type: Object, default: null },
  /** The kinds this build can write to, with their fields. */
  kinds: { type: Array, default: () => [] },
})
const open = defineModel('open', { type: Boolean, default: false })

const auth = useAuthStore()
const config = useConfigStore()

const blank = () => ({
  id: '',
  description: '',
  kind: props.kinds[0]?.kind ?? '',
  settings: {},
  domains: '',
  propagationSeconds: 0,
})

const form = ref(blank())
/** What Test found; cleared when the form changes under it. */
const tested = ref(null)
const testing = ref(false)
const testError = ref('')

watch(
  () => [open.value, props.provider, props.kinds],
  () => {
    if (!open.value) return
    const p = props.provider
    form.value = p
      ? { ...blank(), ...p, settings: { ...(p.settings ?? {}) }, domains: joinList(p.domains) }
      : blank()
  },
  { immediate: true },
)

const kind = computed(() => props.kinds.find((k) => k.kind === form.value.kind))
/** The fields the chosen kind takes; the dialog is built from them. */
const fields = computed(() => kind.value?.fields ?? [])
/** Only a kind that keeps dynamic DNS records can be tried. */
const testable = computed(() => Boolean(kind.value?.dynamicDns))

watch(
  () => JSON.stringify(form.value),
  () => {
    tested.value = null
    testError.value = ''
  },
)

const valid = computed(
  () =>
    Boolean(form.value.id) &&
    Boolean(form.value.kind) &&
    fields.value.every((f) => !f.required || form.value.settings[f.key]),
)

/** The provider the form describes, as the draft keeps it. */
function built() {
  const f = form.value
  const out = { id: f.id.trim(), kind: f.kind, settings: {} }
  if (f.description) out.description = f.description
  const domains = [...new Set(parseList(f.domains).map((d) => d.toLowerCase()))]
  if (domains.length) out.domains = domains
  // Only the chosen kind's fields go into the draft; a setting left over
  // from another kind is refused by the server.
  for (const field of fields.value) {
    const value = f.settings[field.key]
    if (value) out.settings[field.key] = value
  }
  if (f.propagationSeconds) out.propagationSeconds = Number(f.propagationSeconds)
  return out
}

function save() {
  config.upsertDnsProvider(built(), props.provider?.id)
  open.value = false
}

async function test() {
  tested.value = null
  testError.value = ''
  testing.value = true
  try {
    tested.value = await api.dnsProviders.test(built())
  } catch (e) {
    testError.value = errorMessage(e)
  } finally {
    testing.value = false
  }
}

/** What the credentials can see, as a sentence. */
const sees = computed(() => {
  const t = tested.value
  if (!t) return ''
  const label = kind.value?.label ?? 'The provider'
  const zones = t.zones ?? []
  if (!zones.length) return `${label} accepts the credentials, and they see no zones.`
  const rest = zones.length - 3 + (t.more ?? 0)
  const listed = zones.slice(0, 3).join(', ') + (rest > 0 ? ` and ${rest} more` : '')
  return `${label} accepts the credentials. They see ${listed}.`
})
</script>

<template>
  <AppDialog v-model:open="open" :title="provider ? `Provider ${provider.id}` : 'Add DNS provider'">
    <form class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="prov-id" label="Name">
          <input
            id="prov-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
            :disabled="Boolean(provider)"
          />
        </FormField>
        <FormField id="prov-desc" label="Description">
          <input id="prov-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <FormField
        id="prov-kind"
        label="Kind"
        :hint="auth.isOperator ? 'Only an admin can change the kind and credentials.' : undefined"
      >
        <select id="prov-kind" v-model="form.kind" class="input" :disabled="!auth.isAdmin">
          <option v-for="k in kinds" :key="k.kind" :value="k.kind">{{ k.label }}</option>
        </select>
      </FormField>

      <FormField id="prov-domains" label="Domains" hint="One per line, as the provider names them.">
        <textarea
          id="prov-domains"
          v-model="form.domains"
          class="input h-20 font-mono"
          spellcheck="false"
          placeholder="example.com"
        />
      </FormField>

      <FormField
        v-for="f in fields"
        :id="`prov-${f.key}`"
        :key="f.key"
        :label="f.label"
        :hint="f.hint"
      >
        <input
          :id="`prov-${f.key}`"
          v-model="form.settings[f.key]"
          :type="f.secret ? 'password' : 'text'"
          class="input font-mono"
          :required="f.required"
          :disabled="!auth.isAdmin"
          autocomplete="off"
          spellcheck="false"
        />
      </FormField>

      <FormField id="prov-wait" label="Propagation wait" hint="Seconds. 0 is the provider's own.">
        <input
          id="prov-wait"
          v-model.number="form.propagationSeconds"
          type="number"
          min="0"
          max="600"
          class="input w-24 font-mono max-sm:w-full"
        />
      </FormField>

      <ul v-if="tested" class="space-y-1 text-sm" role="status">
        <li>{{ sees }}</li>
        <li v-for="d in tested.domains" :key="d.domain" :class="{ 'text-bad': d.error }">
          <span class="font-mono">{{ d.domain }}:</span>&nbsp;{{ d.error || 'found.' }}
        </li>
      </ul>
      <ErrorLine v-if="testError" class="text-sm">{{ testError }}</ErrorLine>

      <div class="flex justify-end gap-2 pt-2">
        <ActionButton
          v-if="testable && !auth.readOnly"
          class="mr-auto"
          label="Test"
          busy-label="Testing…"
          :busy="testing"
          :disabled="!valid"
          @click="test"
        />
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!valid">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
