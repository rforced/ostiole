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

/** Interfaces with IPv6 switched on and no scope yet (unless editing). */
const candidates = computed(() =>
  config.interfaces.filter(
    (i) =>
      i.ipv6?.mode &&
      i.ipv6.mode !== 'none' &&
      (props.scope?.interface === i.name ||
        !(config.draft.services?.dhcp?.v6 ?? []).some((s) => s.interface === i.name)),
  ),
)

const managed = computed(() => form.value.mode === 'managed')

/** True when the chosen interface takes its prefix from an upstream. */
const delegated = computed(
  () => config.findInterface(form.value.interface)?.ipv6?.mode === 'delegated',
)

function blank() {
  return {
    interface: '',
    enabled: true,
    mode: 'slaac',
    rangeStart: '',
    rangeEnd: '',
    leaseTime: '12h',
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

/** Managed mode needs a pool; suggest the usual host range. */
watch(managed, (on) => {
  if (!on) return
  if (!form.value.rangeStart) form.value.rangeStart = '::100'
  if (!form.value.rangeEnd) form.value.rangeEnd = '::1ff'
})

function save() {
  const f = form.value
  const out = { interface: f.interface, enabled: f.enabled, mode: f.mode }
  if (f.mode === 'managed') {
    out.rangeStart = f.rangeStart.trim()
    out.rangeEnd = f.rangeEnd.trim()
  }
  if (f.leaseTime) out.leaseTime = f.leaseTime.trim()
  const dns = parseList(f.dns)
  if (dns.length) out.dns = dns
  if (f.domain) out.domain = f.domain.trim()
  config.upsertV6Scope(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="scope ? `IPv6 on ${scope.interface}` : 'New IPv6 advertisement'"
    description="The prefix comes from the interface itself, so SLAAC or a delegated prefix keeps working."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="v6-if" label="Interface">
        <select id="v6-if" v-model="form.interface" class="input" required :disabled="!!scope">
          <option value="" disabled>Choose</option>
          <option v-for="i in candidates" :key="i.name" :value="i.name">
            {{ i.name }} (IPv6 {{ i.ipv6.mode }})
          </option>
        </select>
      </FormField>
      <FormField id="v6-mode" label="Mode">
        <select id="v6-mode" v-model="form.mode" class="input">
          <option value="slaac">SLAAC — advertise the prefix, hosts pick their own address</option>
          <option value="stateless">
            Stateless — SLAAC addresses, DHCPv6 for DNS and the domain
          </option>
          <option value="managed">Managed — hand out addresses over DHCPv6</option>
        </select>
      </FormField>
      <div v-if="managed" class="grid gap-4 sm:grid-cols-2">
        <FormField id="v6-start" label="Range start" hint="Host part, e.g. ::100">
          <input
            id="v6-start"
            v-model="form.rangeStart"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="v6-end" label="Range end" hint="Host part, e.g. ::1ff">
          <input
            id="v6-end"
            v-model="form.rangeEnd"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
      </div>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="v6-lease" label="Lease time" hint="Also the advertised prefix lifetime.">
          <input
            id="v6-lease"
            v-model="form.leaseTime"
            class="input w-32 font-mono"
            spellcheck="false"
          />
        </FormField>
        <FormField
          id="v6-dns"
          label="DNS servers"
          :hint="
            delegated
              ? 'This interface takes its prefix from upstream, so its address is not known in advance and hosts using SLAAC alone will not learn a resolver. Name one here.'
              : 'Empty: this box when the DNS service is on.'
          "
        >
          <input id="v6-dns" v-model="form.dns" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField id="v6-domain" label="Domain" hint="Empty: the DNS service's domain.">
          <input id="v6-domain" v-model="form.domain" class="input font-mono" spellcheck="false" />
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
