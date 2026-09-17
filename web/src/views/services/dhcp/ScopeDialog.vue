<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ scope: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

/** Interfaces that can serve DHCP: static IPv4 and not already scoped (unless editing). */
const candidates = computed(() =>
  config.interfaces.filter(
    (i) =>
      i.ipv4?.mode === 'static' &&
      (props.scope?.interface === i.name ||
        !(config.draft.services?.dhcp?.scopes ?? []).some((s) => s.interface === i.name)),
  ),
)

function blank() {
  return {
    interface: '',
    enabled: true,
    rangeStart: '',
    rangeEnd: '',
    leaseTime: '12h',
    gateway: '',
    dns: '',
    domain: '',
  }
}

watch(
  () => [open.value, props.scope],
  () => {
    if (!open.value) return
    const s = props.scope
    form.value = s ? { ...blank(), ...s, dns: (s.dns ?? []).join(', ') } : blank()
    if (!s && candidates.value.length === 1) form.value.interface = candidates.value[0].name
  },
  { immediate: true },
)

/** Suggest hosts 100-199 of the interface's /24 when the fields are empty. */
watch(
  () => form.value.interface,
  (name) => {
    const iface = config.findInterface(name)
    if (!iface?.ipv4?.address || form.value.rangeStart) return
    const [addr, bits] = iface.ipv4.address.split('/')
    if (Number(bits) > 24) return
    const octets = addr.split('.')
    if (octets.length !== 4) return
    form.value.rangeStart = `${octets[0]}.${octets[1]}.${octets[2]}.100`
    form.value.rangeEnd = `${octets[0]}.${octets[1]}.${octets[2]}.199`
  },
)

function save() {
  const f = form.value
  const out = {
    interface: f.interface,
    enabled: f.enabled,
    rangeStart: f.rangeStart.trim(),
    rangeEnd: f.rangeEnd.trim(),
  }
  if (f.leaseTime) out.leaseTime = f.leaseTime.trim()
  if (f.gateway) out.gateway = f.gateway.trim()
  const dns = parseList(f.dns)
  if (dns.length) out.dns = dns
  if (f.domain) out.domain = f.domain.trim()
  config.upsertScope(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="scope ? `DHCP on ${scope.interface}` : 'New DHCP scope'"
    description="One pool per interface. The interface needs a static IPv4 address."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="sc-if" label="Interface">
        <select id="sc-if" v-model="form.interface" class="input" required :disabled="!!scope">
          <option value="" disabled>Choose</option>
          <option v-for="i in candidates" :key="i.name" :value="i.name">
            {{ i.name }} ({{ i.ipv4.address }})
          </option>
        </select>
      </FormField>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="sc-start" label="Range start">
          <input
            id="sc-start"
            v-model="form.rangeStart"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="sc-end" label="Range end">
          <input
            id="sc-end"
            v-model="form.rangeEnd"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="sc-lease" label="Lease time" hint="e.g. 12h, 2d, infinite">
          <input
            id="sc-lease"
            v-model="form.leaseTime"
            class="input w-32 font-mono"
            spellcheck="false"
          />
        </FormField>
        <FormField id="sc-gw" label="Gateway" hint="Empty: this router.">
          <input id="sc-gw" v-model="form.gateway" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField
          id="sc-dns"
          label="DNS servers"
          hint="Empty: this router when the DNS service is on, else the system resolvers."
        >
          <input id="sc-dns" v-model="form.dns" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField id="sc-domain" label="Domain" hint="Empty: the DNS service's domain.">
          <input id="sc-domain" v-model="form.domain" class="input font-mono" spellcheck="false" />
        </FormField>
      </div>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.interface">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
