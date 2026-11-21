<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { interfaceLabel, isTunnel } from '@/lib/interfaces'
import type { Gateway, TailscalePeer } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'

interface GatewayForm {
  name: string
  description: string
  enabled: boolean
  interface: string
  address: string
  exitNode: string
  monitor: string
  priority: number
  slow: number | ''
  lossy: number | ''
  every: number | ''
  down: number | ''
  up: number | ''
}

const props = defineProps({
  gateway: { type: Object as PropType<Gateway | null>, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref<GatewayForm>(blank())
const exitNodePeers = ref<TailscalePeer[]>([])

function blank(): GatewayForm {
  return {
    name: '',
    description: '',
    enabled: true,
    interface: '',
    address: '',
    exitNode: '',
    monitor: '',
    priority: 0,
    slow: '',
    lossy: '',
    every: '',
    down: '',
    up: '',
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
          every: props.gateway.probeEverySeconds ?? '',
          down: props.gateway.downAfterProbes ?? '',
          up: props.gateway.upAfterProbes ?? '',
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

const selectedIface = computed(() => config.interfaces.find((i) => i.name === form.value.interface))
/** A WireGuard or OpenVPN tunnel: with no address, rules send traffic into it. */
const onTunnel = computed(() => isTunnel(selectedIface.value))
/** The Tailscale interface: gateways on it route through an exit node. */
const onTailscale = computed(() => Boolean(selectedIface.value?.tailscale))
const tunnelGateway = computed(
  () => (onTunnel.value && !form.value.address.trim()) || onTailscale.value,
)

/** What gets saved for a Tailscale peer: its MagicDNS label, falling back to hostName. */
function peerExitValue(p: TailscalePeer): string {
  const label = (p.dnsName ?? '').replace(/\.$/, '').split('.')[0]
  return label || p.hostName
}

const exitNodeOptions = computed(() =>
  exitNodePeers.value.map((p) => {
    const value = peerExitValue(p)
    const label = p.online ? value : `${value} (offline)`
    return { value, label }
  }),
)

const hasSavedExitOption = computed(
  () =>
    Boolean(form.value.exitNode) &&
    exitNodeOptions.value.some((o) => o.value === form.value.exitNode),
)

watch(
  () => [open.value, onTailscale.value] as const,
  async ([isOpen, isTS]) => {
    if (!isOpen || !isTS) {
      exitNodePeers.value = []
      return
    }
    try {
      const st = await api.tailscale.status()
      exitNodePeers.value = st.running ? (st.peers ?? []).filter((p) => p.exitNode) : []
    } catch {
      exitNodePeers.value = []
    }
  },
  { immediate: true },
)

/**
 * The resolver's first upstream the gateway can probe, offered as its
 * monitor since the router talks to it already. Recursive lookups have
 * none.
 */
const upstream = computed(() => {
  const dns = config.draft?.services?.dns
  let list: string[] = []
  if (dns?.resolver === 'tls') list = (dns.tlsUpstreams ?? []).map((u) => u.address)
  else if (dns?.resolver !== 'recursive')
    list = dns?.upstreams?.length ? dns.upstreams : (config.draft?.system?.dnsServers ?? [])
  const v6 = (a: string) => a.includes(':')
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
  const out: Gateway = { name: f.name.trim(), enabled: f.enabled, interface: f.interface }
  // A tunnel or exit-node gateway never carries the default route.
  if (!tunnelGateway.value && Number(f.priority) > 0) out.priority = Number(f.priority)
  if (f.description) out.description = f.description
  if (onTailscale.value) {
    if (f.exitNode.trim()) out.exitNode = f.exitNode.trim()
  } else if (f.address) {
    out.address = f.address.trim()
  }
  if (f.monitor) out.monitor = f.monitor.trim()
  // Empty keeps the default; 0 turns the warning off.
  if (f.slow !== '' && f.slow != null) out.slowAboveMs = Number(f.slow)
  if (f.lossy !== '' && f.lossy != null) out.lossyAbovePercent = Number(f.lossy)
  if (f.every !== '' && f.every != null) out.probeEverySeconds = Number(f.every)
  if (f.down !== '' && f.down != null) out.downAfterProbes = Number(f.down)
  if (f.up !== '' && f.up != null) out.upAfterProbes = Number(f.up)
  config.upsertGateway(out, props.gateway?.name ?? out.name)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="gateway ? `Gateway ${gateway.name}` : 'Add gateway'"
    :description="
      onTailscale
        ? 'With the exit node down, its traffic is dropped.'
        : tunnelGateway
          ? 'With the tunnel down, its traffic is dropped.'
          : 'The lowest priority that answers its monitor carries the default route. The others wait.'
    "
  >
    <form id="gateway-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="gw-name" label="Name" hint="Lower case, like wan1.">
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
          v-if="onTailscale"
          id="gw-exit-node"
          label="Exit node"
          hint="One per router. The exit node is a tailnet peer and can reach whatever its zone's rules allow, including router DNS."
        >
          <select
            v-if="exitNodeOptions.length"
            id="gw-exit-node"
            v-model="form.exitNode"
            class="input font-mono"
            required
          >
            <option value="" disabled>Choose</option>
            <option v-if="form.exitNode && !hasSavedExitOption" :value="form.exitNode">
              {{ form.exitNode }}
            </option>
            <option v-for="o in exitNodeOptions" :key="o.value" :value="o.value">
              {{ o.label }}
            </option>
          </select>
          <input
            v-else
            id="gw-exit-node"
            v-model="form.exitNode"
            class="input font-mono"
            required
            spellcheck="false"
            placeholder="ams-1 or 100.64.x.y"
          />
        </FormField>
        <FormField
          v-else
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
            onTailscale
              ? 'Empty pings the exit node. An internet address proves the whole path.'
              : tunnelGateway
                ? 'An IPv4 address beyond the tunnel. The provider\'s DNS server keeps the probe inside its network.'
                : 'Empty probes the next hop, which says the line is up. An address past your provider measures the path beyond it too.'
          "
        >
          <input
            id="gw-monitor"
            v-model="form.monitor"
            class="input font-mono"
            spellcheck="false"
            :placeholder="onTailscale ? 'the exit node' : tunnelGateway ? '' : 'the next hop'"
            :required="tunnelGateway && !onTailscale"
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
          id="gw-every"
          label="Probe every (seconds)"
          hint="30 is the default, 5 at least."
        >
          <input
            id="gw-every"
            v-model="form.every"
            type="number"
            min="5"
            max="300"
            placeholder="30"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
        <FormField
          id="gw-down"
          label="Down after (lost probes)"
          hint="3 in a row is the default, 20 at most."
        >
          <input
            id="gw-down"
            v-model="form.down"
            type="number"
            min="1"
            max="20"
            placeholder="3"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
        <FormField
          id="gw-up"
          label="Up after (answered probes)"
          hint="2 in a row is the default, 20 at most."
        >
          <input
            id="gw-up"
            v-model="form.up"
            type="number"
            min="1"
            max="20"
            placeholder="2"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
        <FormField
          id="gw-slow"
          label="Slow above (ms)"
          hint="The mean round trip over the minutes that hold the last ten probes. 0 turns it off."
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
          hint="The share lost over the minutes that hold the last ten probes. 0 turns it off."
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
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="gateway-form" class="btn-primary" :disabled="!form.interface">
        Save to draft
      </button>
    </template>
  </AppDialog>
</template>
