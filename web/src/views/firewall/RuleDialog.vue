<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import EndpointFields from '@/views/firewall/EndpointFields.vue'
import { TIERS } from '@/views/firewall/shaping/tiers'

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
    notAddresses: Boolean(ep.notAddresses),
    portMode: ep.portAlias ? 'alias' : ep.ports?.length ? 'ports' : 'any',
    ports: (ep.ports ?? []).join(', '),
    portAlias: ep.portAlias ?? '',
    notPorts: Boolean(ep.notPorts),
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
    schedule: '',
    gateway: '',
    priority: '',
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
          gateway: r.gateway ?? '',
          priority: r.priority ?? '',
          source: endpointForm(r.source),
          destination: endpointForm(r.destination),
        }
      : blank()
  },
  { immediate: true },
)

const portsAllowed = computed(() => ['tcp', 'udp', 'tcp+udp'].includes(form.value.protocol))

// A gateway decides where the traffic leaves, so it cannot be combined
// with a rule that matches on the zone it leaves by, and only an accept
// rule sends anything anywhere.
const gatewayAllowed = computed(() => form.value.action === 'accept' && !form.value.destZone)

watch(gatewayAllowed, (ok) => {
  if (!ok) form.value.gateway = ''
})

// A priority decides what a flow yields to, so there has to be a flow: a
// rule that drops the traffic has nothing to prioritise.
const priorityAllowed = computed(() => form.value.action === 'accept')

watch(priorityAllowed, (ok) => {
  if (!ok) form.value.priority = ''
})

function endpointOut(f, allowPorts) {
  const out = {}
  if (f.mode === 'addresses') out.addresses = parseList(f.addresses)
  if (f.mode === 'alias') out.alias = f.alias
  if (f.mode === 'self') out.self = true
  if (allowPorts && f.portMode === 'ports') out.ports = parseList(f.ports)
  if (allowPorts && f.portMode === 'alias') out.portAlias = f.portAlias
  // An inversion only travels with something to invert; "not anything" is
  // rejected by the server rather than silently meaning "everything".
  if (f.notAddresses && f.mode !== 'any') out.notAddresses = true
  if (allowPorts && f.notPorts && f.portMode !== 'any') out.notPorts = true
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
  if (f.schedule) out.schedule = f.schedule
  if (f.gateway && gatewayAllowed.value) out.gateway = f.gateway
  if (f.priority && priorityAllowed.value) out.priority = f.priority
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
        <FormField id="rule-destzone" label="Leaving via zone" hint="Forwarded traffic only.">
          <select id="rule-destzone" v-model="form.destZone" class="input">
            <option value="">Any</option>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
        <FormField
          v-if="config.schedules.length"
          id="rule-schedule"
          label="Schedule"
          hint="Outside the window the rule does not match."
        >
          <select id="rule-schedule" v-model="form.schedule" class="input">
            <option value="">Always</option>
            <option v-for="s in config.schedules" :key="s.name" :value="s.name">
              {{ s.name }} ({{ s.start }}–{{ s.end }})
            </option>
          </select>
        </FormField>
        <FormField
          v-if="config.routeTargets.length"
          id="rule-gateway"
          label="Route through"
          :hint="
            gatewayAllowed
              ? 'Sends matching traffic out this gateway instead of the default route.'
              : 'Only an accept rule that does not pick a leaving zone can choose a gateway.'
          "
        >
          <select
            id="rule-gateway"
            v-model="form.gateway"
            class="input"
            :disabled="!gatewayAllowed"
          >
            <option value="">Default route</option>
            <option v-for="t in config.routeTargets" :key="t.name" :value="t.name">
              {{ t.name }}{{ t.kind === 'group' ? ' (group)' : ''
              }}{{ t.enabled ? '' : ' (disabled)' }}
            </option>
          </select>
        </FormField>
        <FormField
          v-if="config.shapedInterfaces.length"
          id="rule-priority"
          label="Priority"
          :hint="
            priorityAllowed
              ? 'Higher goes first when the line is full. Bulk yields to everything.'
              : 'Only an accept rule can set a priority.'
          "
        >
          <select
            id="rule-priority"
            v-model="form.priority"
            class="input"
            :disabled="!priorityAllowed"
          >
            <option value="">Normal (unset)</option>
            <option v-for="t in TIERS" :key="t.value" :value="t.value">
              {{ t.label }} — {{ t.hint }}
            </option>
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
