<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import AppNotice from '@/components/AppNotice.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import ZoneField from '@/components/ZoneField.vue'
import type { Interface } from '@/lib/types.gen'
import { ADMIN_ONLY, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

interface Link {
  name: string
  mtu?: number
}

type LogDropsChoice = 'inherit' | 'on' | 'off'
type InterfaceForm = Omit<Interface, 'logDrops'> & { logDrops: LogDropsChoice }

const props = defineProps({
  /** Interface config being edited, or a fresh one for an unconfigured link. */
  iface: { type: Object as PropType<Interface | null>, default: null },
  /** Live links from the kernel, for the MTU this interface is running at. */
  links: { type: Array as PropType<Link[]>, default: () => [] },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits<{ saved: [iface: Interface] }>()

const config = useConfigStore()
const auth = useAuthStore()
const form = ref<InterfaceForm>(blank())
const zoneField = ref<InstanceType<typeof ZoneField> | null>(null)

/**
 * Switching an interface on or off in a zone with anti-lockout changes
 * where the router is managed from, which only an admin may.
 */
const enabledLocked = computed(() => {
  const zone = config.interfaces.find((i) => i.name === form.value.name)?.zone
  return auth.isOperator && config.zones.some((z) => z.name === zone && z.antiLockout)
})

function blank(): InterfaceForm {
  return {
    name: '',
    description: '',
    zone: '',
    enabled: true,
    ipv4: { mode: 'none', address: '', gateway: '', vendorClass: '' },
    ipv6: {
      mode: 'none',
      address: '',
      gateway: '',
      prefixHint: '',
      delegatedFrom: '',
      subnetId: 0,
    },
    mtu: 0,
    macAddress: '',
    logDrops: 'inherit',
    blockPrivate: false,
    blockBogons: false,
    allowUnroutedIPv6: false,
  }
}

/** What the kernel has for this interface, when it exists yet. */
const live = computed(() => props.links.find((l) => l.name === form.value.name) ?? null)

/** tailscaled addresses its own interface and sets its MTU from the tailnet; an OpenVPN server addresses its tunnel. */
const daemonOwned = computed(() => Boolean(form.value.tailscale || form.value.openvpn))

/** Only a card has a hardware address to answer to instead of its own. */
const physical = computed(() => {
  const f = form.value
  return !(
    f.vlan ||
    f.bridge ||
    f.bond ||
    f.wireguard ||
    f.openvpn ||
    f.pppoe ||
    f.tailscale ||
    f.wireless
  )
})

/** Plain Ethernet, and what a WireGuard device leaves for its own header. */
const ETHERNET_MTU = 1500
const WIREGUARD_MTU = 1420

/**
 * The MTU this interface would run at with nothing configured: a VLAN
 * inherits its parent's, a tunnel pays for its encapsulation, and anything
 * else is a standard Ethernet frame. Configuring exactly this is the same
 * as configuring nothing, so save() leaves it out.
 */
const defaultMtu = computed(() => {
  const f = form.value
  if (f.wireguard) return WIREGUARD_MTU
  const parentName = f.vlan?.parent
  if (!parentName) return ETHERNET_MTU
  const parent = config.interfaces.find((i) => i.name === parentName)
  return parent?.mtu || props.links.find((l) => l.name === parentName)?.mtu || ETHERNET_MTU
})

const mtuHint = computed(() => {
  const d = defaultMtu.value
  const parent = form.value.vlan?.parent
  return parent
    ? `The default is ${d}, inherited from ${parent}. Lower it only when the path needs it.`
    : `The default is ${d}. Lower it only when the path needs it.`
})

/**
 * The link is running an MTU nothing in the configuration asks for, so a
 * reboot would lose it. The field is pre-filled with that number, which
 * means saving is what keeps it.
 */
const pinsLiveMtu = computed(
  () =>
    !props.iface?.mtu &&
    live.value !== null &&
    live.value.mtu !== defaultMtu.value &&
    form.value.mtu === live.value.mtu,
)

watch(
  () => [open.value, props.iface],
  () => {
    if (!open.value) return
    const src = props.iface ?? blank()
    form.value = {
      ...blank(),
      ...(JSON.parse(JSON.stringify(src)) as Interface),
      logDrops: src.logDrops === undefined ? 'inherit' : src.logDrops ? 'on' : 'off',
      ipv4: { address: '', gateway: '', ...src.ipv4, mode: src.ipv4?.mode ?? 'none' },
      ipv6: {
        address: '',
        gateway: '',
        prefixHint: '',
        delegatedFrom: '',
        subnetId: 0,
        ...src.ipv6,
        mode: src.ipv6?.mode ?? 'none',
      },
    }
    // An unset MTU is whatever the link is running at, which is a number
    // worth seeing; 0 only ever looked like something was broken.
    if (!form.value.mtu && !daemonOwned.value) {
      form.value.mtu = live.value?.mtu || defaultMtu.value
    }
  },
  { immediate: true },
)

const title = computed(() => `Interface ${form.value.name}`)

/** What the system setting does when an interface says nothing. */
const systemLogsDrops = computed(() => config.draft?.system?.management?.logDefaultDrops ?? false)

/** Blocking by source address belongs on an interface facing the internet. */
const external = computed(
  () => config.zones.find((z) => z.name === form.value.zone)?.external ?? false,
)

/**
 * The router holds IPv6 sources to the routes on an inside interface it
 * numbers itself. Tunnels, PPPoE and a gateway's link are left out, as
 * the renderer leaves them out.
 */
const sourceChecked = computed(() => {
  const f = form.value
  if (!f.zone || external.value) return false
  if (f.ipv6.mode !== 'static' && f.ipv6.mode !== 'delegated') return false
  if (f.wireguard || f.openvpn || f.tailscale || f.pppoe) return false
  return !config.gateways.some((g) => g.interface === f.name)
})

/** An interface on a private network would cut itself off. */
const privateItself = computed(() => {
  const addr = form.value.ipv4.address
  if (form.value.ipv4.mode !== 'static' || !addr) return false
  return /^(10\.|127\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(addr)
})

/** Interfaces that ask an upstream for a prefix, which this one can share. */
const upstreams = computed(() =>
  config.interfaces.filter((i) => i.name !== form.value.name && i.ipv6?.prefixHint),
)

function save() {
  if (!zoneField.value?.commit()) return
  const out = JSON.parse(JSON.stringify(form.value)) as Interface
  const logDrops = form.value.logDrops
  if (logDrops === 'inherit') delete out.logDrops
  else out.logDrops = logDrops === 'on'
  if (!out.blockPrivate) delete out.blockPrivate
  if (!out.blockBogons) delete out.blockBogons
  if (!out.allowUnroutedIPv6) delete out.allowUnroutedIPv6
  if (out.ipv4.mode !== 'static') out.ipv4.address = ''
  if (out.ipv6.mode !== 'static') out.ipv6.address = ''
  if (out.ipv6.mode !== 'dhcp') out.ipv6.prefixHint = ''
  if (out.ipv6.mode !== 'delegated') {
    out.ipv6.delegatedFrom = ''
    out.ipv6.subnetId = 0
  }
  for (const fam of ['ipv4', 'ipv6'] as const) {
    if (!out[fam].address) delete out[fam].address
    if (!out[fam].gateway) delete out[fam].gateway
  }
  if (!out.ipv6.prefixHint) delete out.ipv6.prefixHint
  if (!out.ipv6.delegatedFrom) delete out.ipv6.delegatedFrom
  out.ipv6.subnetId = Number(out.ipv6.subnetId) || 0
  if (!out.ipv6.subnetId) delete out.ipv6.subnetId
  if (!out.zone) delete out.zone
  if (!out.description) delete out.description
  out.mtu = Number(out.mtu) || 0
  if (!out.mtu || (out.mtu === defaultMtu.value && !props.iface?.mtu)) delete out.mtu
  out.macAddress = (out.macAddress || '').trim()
  if (!out.macAddress || !physical.value) delete out.macAddress
  if (out.ipv4.mode !== 'dhcp') delete out.ipv4.sendHostname
  else if (!out.ipv4.sendHostname) delete out.ipv4.sendHostname
  out.ipv4.vendorClass = (out.ipv4.vendorClass || '').trim()
  if (!out.ipv4.vendorClass || out.ipv4.mode !== 'dhcp') delete out.ipv4.vendorClass
  if (out.ipv6.mode !== 'slaac' && out.ipv6.mode !== 'dhcp') delete out.ipv6.temporaryAddresses
  else if (!out.ipv6.temporaryAddresses) delete out.ipv6.temporaryAddresses
  config.upsertInterface(out)
  emit('saved', out)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="title">
    <form id="interface-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField
          id="if-desc"
          label="Description"
          hint="Shown wherever this interface is picked, like Office LAN."
        >
          <input id="if-desc" v-model="form.description" class="input" />
        </FormField>
        <ZoneField id="if-zone" ref="zoneField" v-model="form.zone" :iface="form.name" />
      </div>

      <ToggleRow
        v-model="form.enabled"
        label="Enabled"
        :disabled="enabledLocked"
        :hint="enabledLocked ? ADMIN_ONLY : ''"
      />

      <p v-if="form.openvpn" class="text-sm text-ink-muted">
        The server gives this tunnel its addresses. Its settings and MTU are under
        <RouterLink to="/vpn/openvpn" class="link">VPN › OpenVPN</RouterLink>.
      </p>
      <p v-else-if="daemonOwned" class="text-sm text-ink-muted">
        The tailnet gives this interface its addresses and its MTU. Its settings are under
        <RouterLink to="/vpn/tailscale" class="link">VPN › Tailscale › Settings</RouterLink>.
      </p>

      <p v-if="form.wireless" class="text-sm text-ink-muted">
        Its network and security are under
        <RouterLink to="/wireless#networks" class="link">Wireless › Networks</RouterLink>.
      </p>

      <fieldset v-if="!daemonOwned" class="field-group">
        <legend>IPv4</legend>
        <FormField id="if-v4-mode" label="Mode">
          <select id="if-v4-mode" v-model="form.ipv4.mode" class="input">
            <option value="none">None</option>
            <option value="static">Static</option>
            <option value="dhcp">DHCP client</option>
          </select>
        </FormField>
        <div v-if="form.ipv4.mode === 'static'" class="fields">
          <FormField
            id="if-v4-addr"
            label="Address (CIDR)"
            hint="Address and prefix length, like 192.168.1.1/24."
          >
            <input id="if-v4-addr" v-model="form.ipv4.address" class="input font-mono" required />
          </FormField>
          <FormField id="if-v4-gw" label="Gateway" hint="Only for upstream links.">
            <input
              id="if-v4-gw"
              v-model="form.ipv4.gateway"
              class="input font-mono"
              placeholder="optional"
            />
          </FormField>
        </div>
        <ToggleRow
          v-if="form.ipv4.mode === 'dhcp'"
          id="if-v4-hostname"
          v-model="form.ipv4.sendHostname"
          label="Send hostname"
          hint="Off sends no name to the provider."
        />
        <FormField
          v-if="form.ipv4.mode === 'dhcp'"
          id="if-v4-vendor"
          label="Vendor class"
          hint="Only when the provider requires one. Empty sends none."
        >
          <input
            id="if-v4-vendor"
            v-model="form.ipv4.vendorClass"
            class="input w-48 font-mono max-sm:w-full"
            placeholder="optional"
            spellcheck="false"
            autocapitalize="none"
          />
        </FormField>
      </fieldset>

      <fieldset v-if="!daemonOwned" class="field-group">
        <legend>IPv6</legend>
        <FormField id="if-v6-mode" label="Mode">
          <select id="if-v6-mode" v-model="form.ipv6.mode" class="input">
            <option value="none">None</option>
            <option value="static">Static</option>
            <option value="slaac">SLAAC (router advertisements)</option>
            <option value="dhcp">DHCPv6</option>
            <option value="delegated" :disabled="!upstreams.length">
              Delegated (a subnet of an upstream prefix)
            </option>
          </select>
        </FormField>
        <FormField
          v-if="form.ipv6.mode === 'dhcp'"
          id="if-v6-hint"
          label="Ask for a prefix"
          hint="The prefix size to request upstream, like ::/56. Empty asks for nothing."
        >
          <input
            id="if-v6-hint"
            v-model="form.ipv6.prefixHint"
            class="input font-mono"
            spellcheck="false"
          />
        </FormField>
        <div v-if="form.ipv6.mode === 'delegated'" class="fields">
          <FormField id="if-v6-from" label="Prefix from">
            <select id="if-v6-from" v-model="form.ipv6.delegatedFrom" class="input" required>
              <option value="" disabled>Choose</option>
              <option v-for="u in upstreams" :key="u.name" :value="u.name">
                {{ u.name }} ({{ u.ipv6.prefixHint }})
              </option>
            </select>
          </FormField>
          <FormField
            id="if-v6-subnet"
            label="Subnet"
            hint="0, the first /64 of the prefix, is the default. No two interfaces on one prefix take the same one."
          >
            <input
              id="if-v6-subnet"
              v-model.number="form.ipv6.subnetId"
              type="number"
              min="0"
              max="65535"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
        </div>
        <div v-if="form.ipv6.mode === 'static'" class="fields">
          <FormField
            id="if-v6-addr"
            label="Address (CIDR)"
            hint="Address and prefix length, like 2001:db8::1/64."
          >
            <input id="if-v6-addr" v-model="form.ipv6.address" class="input font-mono" required />
          </FormField>
          <FormField id="if-v6-gw" label="Gateway">
            <input
              id="if-v6-gw"
              v-model="form.ipv6.gateway"
              class="input font-mono"
              placeholder="optional"
            />
          </FormField>
        </div>
        <ToggleRow
          v-if="form.ipv6.mode === 'slaac' || form.ipv6.mode === 'dhcp'"
          id="if-v6-temporary"
          v-model="form.ipv6.temporaryAddresses"
          label="Temporary addresses"
          hint="On changes the outgoing address daily."
        />
      </fieldset>

      <FormField v-if="!daemonOwned" id="if-mtu" label="MTU" :hint="mtuHint">
        <input
          id="if-mtu"
          v-model.number="form.mtu"
          type="number"
          min="68"
          max="65535"
          class="input w-32 max-sm:w-full"
        />
      </FormField>
      <p v-if="pinsLiveMtu && !daemonOwned" class="text-sm text-ink-muted">
        {{ form.name }} is running at {{ live?.mtu }}, which nothing in the configuration asks for.
        Saving this keeps it after a reboot.
      </p>

      <FormField v-if="physical" id="if-mac" label="MAC address" hint="Empty keeps the card's own.">
        <input
          id="if-mac"
          v-model="form.macAddress"
          class="input w-48 font-mono max-sm:w-full"
          placeholder="optional"
          spellcheck="false"
          autocapitalize="none"
        />
      </FormField>

      <fieldset class="field-group">
        <legend>Traffic arriving here</legend>
        <FormField
          id="if-logdrops"
          label="Log dropped packets"
          hint="Overrides the system setting for packets arriving on this interface."
        >
          <select id="if-logdrops" v-model="form.logDrops" class="input">
            <option value="inherit">
              Follow the system setting ({{ systemLogsDrops ? 'on' : 'off' }})
            </option>
            <option value="on">Always log</option>
            <option value="off">Never log</option>
          </select>
        </FormField>

        <ToggleRow
          v-model="form.blockPrivate"
          label="Block private and loopback sources"
          hint="Drops traffic arriving from 10/8, 172.16/12, 192.168/16, 127/8, and fc00::/7, but not link-local."
        />
        <AppNotice v-if="form.blockPrivate && privateItself">
          This interface is itself on a private network, so blocking private sources here would cut
          it off.
        </AppNotice>

        <ToggleRow
          v-model="form.blockBogons"
          label="Block bogon sources"
          hint="Drops traffic from prefixes nobody has been allocated. The list is refreshed daily."
        />
        <AppNotice v-if="(form.blockPrivate || form.blockBogons) && form.zone && !external">
          Zone {{ form.zone }} is internal, so blocking by source address here drops legitimate
          traffic.
        </AppNotice>

        <ToggleRow
          v-if="sourceChecked"
          id="if-ipv6-source"
          :model-value="!form.allowUnroutedIPv6"
          label="Check IPv6 sources against the routes"
          hint="Drops IPv6 from sources the router would not send back out this interface. Turn it off for a link that carries one direction of an asymmetric path."
          @update:model-value="form.allowUnroutedIPv6 = !$event"
        />
      </fieldset>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="interface-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
