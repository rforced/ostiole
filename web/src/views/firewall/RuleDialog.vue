<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import EndpointFields from '@/views/firewall/EndpointFields.vue'

const props = defineProps({
  rule: { type: Object, default: null },
  zone: { type: String, required: true },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function endpointForm(ep = {}) {
  return {
    mode: ep.self ? 'self' : ep.alias ? 'alias' : ep.addresses?.length ? 'addresses' : 'any',
    addresses: joinList(ep.addresses),
    alias: ep.alias ?? '',
    portMode: ep.portAlias ? 'alias' : ep.ports?.length ? 'ports' : 'any',
    ports: (ep.ports ?? []).join(', '),
    portAlias: ep.portAlias ?? '',
  }
}

function blank() {
  return {
    id: '',
    description: '',
    enabled: true,
    zone: props.zone,
    destZone: '',
    action: 'accept',
    protocol: 'any',
    log: false,
    source: endpointForm(),
    destination: endpointForm(),
  }
}

watch(
  () => [open.value, props.rule],
  () => {
    if (!open.value) return
    const r = props.rule
    form.value = r
      ? {
          ...blank(),
          ...r,
          destZone: r.destZone ?? '',
          source: endpointForm(r.source),
          destination: endpointForm(r.destination),
        }
      : blank()
  },
  { immediate: true },
)

const portsAllowed = computed(() => ['tcp', 'udp', 'tcp+udp'].includes(form.value.protocol))

function endpointOut(f, allowPorts) {
  const out = {}
  if (f.mode === 'addresses') out.addresses = parseList(f.addresses)
  if (f.mode === 'alias') out.alias = f.alias
  if (f.mode === 'self') out.self = true
  if (allowPorts && f.portMode === 'ports') out.ports = parseList(f.ports)
  if (allowPorts && f.portMode === 'alias') out.portAlias = f.portAlias
  return out
}

function save() {
  const f = form.value
  const out = {
    id: f.id || newId('r'),
    description: f.description,
    enabled: f.enabled,
    zone: f.zone,
    action: f.action,
    protocol: f.protocol,
    source: endpointOut(f.source, portsAllowed.value),
    destination: endpointOut(f.destination, portsAllowed.value),
  }
  if (f.destZone) out.destZone = f.destZone
  if (f.log) out.log = true
  if (!out.description) delete out.description
  config.upsertRule(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="rule ? `Rule ${rule.id}` : `New rule in ${zone}`"
    description="Rules match traffic entering the zone, whether it is for this firewall or forwarded through it."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="rule-desc" label="Description">
        <input id="rule-desc" v-model="form.description" class="input" />
      </FormField>
      <div class="grid gap-4 sm:grid-cols-3">
        <FormField id="rule-action" label="Action">
          <select id="rule-action" v-model="form.action" class="input">
            <option value="accept">Accept</option>
            <option value="drop">Drop</option>
            <option value="reject">Reject</option>
          </select>
        </FormField>
        <FormField id="rule-proto" label="Protocol">
          <select id="rule-proto" v-model="form.protocol" class="input">
            <option value="any">Any</option>
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
            <option value="tcp+udp">TCP + UDP</option>
            <option value="icmp">ICMP / ICMPv6</option>
          </select>
        </FormField>
        <FormField
          id="rule-destzone"
          label="Leaving via zone"
          hint="Optional; forwarded traffic only."
        >
          <select id="rule-destzone" v-model="form.destZone" class="input">
            <option value="">Any</option>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
      </div>

      <EndpointFields v-model="form.source" side="source" :ports-allowed="portsAllowed" />
      <EndpointFields v-model="form.destination" side="destination" :ports-allowed="portsAllowed" />

      <div class="flex flex-wrap gap-4 text-sm">
        <label class="flex items-center gap-2">
          <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
          Enabled
        </label>
        <label class="flex items-center gap-2">
          <input v-model="form.log" type="checkbox" class="size-4 rounded border-neutral-300" /> Log
          matches
        </label>
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
