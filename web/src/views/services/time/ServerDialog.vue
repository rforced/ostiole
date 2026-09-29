<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'

const props = defineProps({
  /** The server being edited; null adds one. */
  server: { type: Object, default: null },
  /** Every host listed now, so the same one is not added twice. */
  hosts: { type: Array, default: () => [] },
})
const emit = defineEmits(['save'])
const open = defineModel('open', { type: Boolean, default: false })

const form = ref(blank())

function blank() {
  return { host: '', nts: true, pool: false }
}

// A server written without a switch has it off; a new one starts signed.
watch(
  () => [open.value, props.server],
  () => {
    if (!open.value) return
    form.value = props.server ? { host: '', nts: false, pool: false, ...props.server } : blank()
  },
  { immediate: true },
)

const host = computed(() => form.value.host.trim())

/** An address rather than a name: IPv6 has colons, IPv4 only digits and dots. */
const isAddress = computed(() => host.value.includes(':') || /^[0-9.]+$/.test(host.value))

const problem = computed(() => {
  const was = (props.server?.host ?? '').toLowerCase()
  const now = host.value.toLowerCase()
  if (now && now !== was && props.hosts.some((h) => h.toLowerCase() === now)) {
    return 'That server is listed already.'
  }
  if (isAddress.value && form.value.nts) return 'Signed answers need a name, not an address.'
  if (isAddress.value && form.value.pool) return 'A pool is a name, not an address.'
  return ''
})

function save() {
  const out = { host: host.value }
  if (form.value.nts) out.nts = true
  if (form.value.pool) out.pool = true
  emit('save', out, props.server?.host ?? '')
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="server ? server.host : 'Add time server'"
    description="The clock follows the servers that agree with each other."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="ntp-host" label="Server" hint="A name or an address, e.g. time.example.com.">
        <input
          id="ntp-host"
          v-model="form.host"
          class="input font-mono"
          required
          spellcheck="false"
          placeholder="time.example.com"
        />
      </FormField>
      <ToggleRow
        v-model="form.nts"
        label="Signed answers (NTS)"
        hint="The server's certificate is checked against its name."
      />
      <ToggleRow v-model="form.pool" label="Pool" hint="Up to four of its servers are asked." />
      <p v-if="problem" role="alert" class="text-sm text-bad">{{ problem }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!host || Boolean(problem)">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
