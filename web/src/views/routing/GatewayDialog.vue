<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { interfaceLabel } from '@/lib/interfaces'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ gateway: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return {
    name: '',
    description: '',
    enabled: true,
    interface: '',
    address: '',
    monitor: '',
    priority: 0,
    slow: '',
    lossy: '',
  }
}

watch(
  () => [open.value, props.gateway],
  () => {
    if (!open.value) return
    form.value = props.gateway
      ? {
          ...blank(),
          ...props.gateway,
          slow: props.gateway.slowAboveMs ?? '',
          lossy: props.gateway.lossyAbovePercent ?? '',
        }
      : blank()
    if (!props.gateway) {
      const wan = config.interfaces.find((i) => {
        const zone = config.zones.find((z) => z.name === i.zone)
        return zone?.external
      })
      if (wan) form.value.interface = wan.name
      form.value.priority = config.gateways.length
    }
  },
  { immediate: true },
)

/** A WireGuard tunnel as the interface: with no address, rules send traffic into it. */
const onTunnel = computed(
  () => !!config.interfaces.find((i) => i.name === form.value.interface)?.wireguard,
)
const tunnelGateway = computed(() => onTunnel.value && !form.value.address.trim())

/**
 * The resolver's first upstream the gateway can probe, offered as its
 * monitor since the router talks to it already. Recursive lookups have
 * none.
 */
const upstream = computed(() => {
  const dns = config.draft?.services?.dns ?? {}
  let list = []
  if (dns.resolver === 'tls') list = (dns.tlsUpstreams ?? []).map((u) => u.address)
  else if (dns.resolver !== 'recursive')
    list = dns.upstreams?.length ? dns.upstreams : (config.draft?.system?.dnsServers ?? [])
  const v6 = (a) => a.includes(':')
  const address = form.value.address.trim()
  return (
    list.find((a) => {
      if (tunnelGateway.value) return !v6(a)
      return !address || v6(address) === v6(a)
    }) ?? ''
  )
})

function save() {
  const f = form.value
  const out = {
    name: f.name.trim(),
    enabled: f.enabled,
    interface: f.interface,
    // A tunnel gateway never carries the default route.
    priority: tunnelGateway.value ? 0 : Number(f.priority) || 0,
  }
  if (f.description) out.description = f.description
  if (f.address) out.address = f.address.trim()
  if (f.monitor) out.monitor = f.monitor.trim()
  // Empty keeps the default; 0 turns the warning off.
  if (f.slow !== '' && f.slow != null) out.slowAboveMs = Number(f.slow)
  if (f.lossy !== '' && f.lossy != null) out.lossyAbovePercent = Number(f.lossy)
  config.upsertGateway(out, props.gateway?.name ?? out.name)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="gateway ? `Gateway ${gateway.name}` : 'Add gateway'"
    :description="
      tunnelGateway
        ? 'With the tunnel down, its traffic is dropped.'
        : 'The lowest priority that answers its monitor carries the default route. The others wait.'
    "
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="gw-name" label="Name" hint="Lower case, e.g. wan1.">
          <input
            id="gw-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="gw-desc" label="Description">
          <input id="gw-desc" v-model="form.description" class="input" />
        </FormField>
        <FormField id="gw-if" label="Interface">
          <select id="gw-if" v-model="form.interface" class="input" required>
            <option value="" disabled>Choose</option>
            <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
              {{ interfaceLabel(i) }}
            </option>
          </select>
        </FormField>
        <FormField
          id="gw-addr"
          label="Gateway address"
          :hint="
            onTunnel
              ? 'Empty sends traffic from rules into the tunnel. The router\'s own traffic keeps the default route.'
              : 'Empty follows DHCP or a router advertisement.'
          "
        >
          <input id="gw-addr" v-model="form.address" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField
          id="gw-monitor"
          label="Monitor address"
          :hint="
            tunnelGateway
              ? 'An IPv4 address beyond the tunnel. The provider\'s DNS server keeps the probe inside its network.'
              : 'Empty probes the next hop, which says the line is up. An address past your provider measures the path beyond it too.'
          "
        >
          <input
            id="gw-monitor"
            v-model="form.monitor"
            class="input font-mono"
            spellcheck="false"
            :placeholder="tunnelGateway ? '' : 'the next hop'"
            :required="tunnelGateway"
          />
          <button
            v-if="upstream && form.monitor.trim() !== upstream"
            type="button"
            class="link mt-1 text-sm"
            @click="form.monitor = upstream"
          >
            Use {{ upstream }}, the DNS upstream
          </button>
        </FormField>
        <FormField
          v-if="!tunnelGateway"
          id="gw-prio"
          label="Priority"
          hint="Lowest wins. Equal priorities share the traffic."
        >
          <input
            id="gw-prio"
            v-model="form.priority"
            type="number"
            min="0"
            max="255"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
        <FormField
          id="gw-slow"
          label="Slow above (ms)"
          hint="A minute's mean round trip. 0 turns it off."
        >
          <input
            id="gw-slow"
            v-model="form.slow"
            type="number"
            min="0"
            max="1999"
            placeholder="200"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
        <FormField
          id="gw-lossy"
          label="Losing packets above (%)"
          hint="A minute's share of probes lost. 0 turns it off."
        >
          <input
            id="gw-lossy"
            v-model="form.lossy"
            type="number"
            min="0"
            max="99"
            placeholder="10"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
      </div>
      <ToggleRow v-model="form.enabled" label="Enabled" />
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.interface">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
