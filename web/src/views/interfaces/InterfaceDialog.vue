<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import AppNotice from '@/components/AppNotice.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** Interface config being edited, or a fresh one for an unconfigured link. */
  iface: { type: Object, default: null },
  /** Live links from the kernel, for the MTU this interface is running at. */
  links: { type: Array, default: () => [] },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['saved'])

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return {
    name: '',
    description: '',
    zone: '',
    enabled: true,
    ipv4: { mode: 'none', address: '', gateway: '' },
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
  }
}

/** What the kernel has for this interface, when it exists yet. */
const live = computed(() => props.links.find((l) => l.name === form.value.name) ?? null)

/** tailscaled addresses its own interface and sets its MTU from the tailnet. */
const daemonOwned = computed(() => Boolean(form.value.tailscale))

/** Only a card has a hardware address to answer to instead of its own. */
const physical = computed(() => {
  const f = form.value
  return !(f.vlan || f.bridge || f.bond || f.wireguard || f.pppoe || f.tailscale || f.wireless)
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
  if (!f.vlan?.parent) return ETHERNET_MTU
  const parent = config.interfaces.find((i) => i.name === f.vlan.parent)
  return parent?.mtu || props.links.find((l) => l.name === f.vlan.parent)?.mtu || ETHERNET_MTU
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
      ...JSON.parse(JSON.stringify(src)),
      logDrops: src.logDrops === undefined ? 'inherit' : src.logDrops ? 'on' : 'off',
      ipv4: { mode: 'none', address: '', gateway: '', ...src.ipv4 },
      ipv6: {
        mode: 'none',
        address: '',
        gateway: '',
        prefixHint: '',
        delegatedFrom: '',
        subnetId: 0,
        ...src.ipv6,
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

/**
 * A zone is the unit firewall rules match on, so interfaces sharing one
 * share a single rule list. That is worth saying out loud here: a VLAN
 * dropped into "lan" silently inherits the LAN rules and can never have its
 * own, which is rarely what someone adding a guest or IOT VLAN wants.
 *
 * NEW_ZONE marks the "New zone…" choice while it is still pending. A real
 * zone name is [a-z][a-z0-9_]*, so the plus can never collide with one.
 */
const NEW_ZONE = '+new'
const newZone = ref('')
const zoneError = ref('')
const creatingZone = computed(() => form.value.zone === NEW_ZONE)
const zoneMates = computed(() =>
  config.interfaces
    .filter((i) => i.name !== form.value.name && i.zone && i.zone === form.value.zone)
    .map((i) => i.name),
)

/** Turns the pending "New zone…" choice into a real zone in the draft. */
function commitZone() {
  if (!creatingZone.value) return true
  const name = newZone.value.trim()
  if (!/^[a-z][a-z0-9_]{0,30}$/.test(name)) {
    zoneError.value =
      'Name must be lowercase letters, digits, or underscores and start with a letter.'
    return false
  }
  if (config.zones.some((z) => z.name === name)) {
    zoneError.value = `Zone ${name} already exists. Pick it from the list.`
    return false
  }
  config.upsertZone({ name })
  form.value.zone = name
  newZone.value = ''
  zoneError.value = ''
  return true
}

/** What the system setting does when an interface says nothing. */
const systemLogsDrops = computed(() => config.draft?.system?.management?.logDefaultDrops ?? false)

/** Blocking by source address belongs on an interface facing the internet. */
const external = computed(
  () => config.zones.find((z) => z.name === form.value.zone)?.external ?? false,
)

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
  if (!commitZone()) return
  const out = JSON.parse(JSON.stringify(form.value))
  if (out.logDrops === 'inherit') delete out.logDrops
  else out.logDrops = out.logDrops === 'on'
  if (!out.blockPrivate) delete out.blockPrivate
  if (!out.blockBogons) delete out.blockBogons
  if (out.ipv4.mode !== 'static') out.ipv4.address = ''
  if (out.ipv6.mode !== 'static') out.ipv6.address = ''
  if (out.ipv6.mode !== 'dhcp') out.ipv6.prefixHint = ''
  if (out.ipv6.mode !== 'delegated') {
    out.ipv6.delegatedFrom = ''
    out.ipv6.subnetId = 0
  }
  for (const fam of ['ipv4', 'ipv6']) {
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
  if (!out.mtu || out.mtu === defaultMtu.value) delete out.mtu
  out.macAddress = (out.macAddress || '').trim()
  if (!out.macAddress || !physical.value) delete out.macAddress
  if (out.ipv4.mode !== 'dhcp') delete out.ipv4.sendHostname
  else if (!out.ipv4.sendHostname) delete out.ipv4.sendHostname
  if (out.ipv6.mode !== 'slaac' && out.ipv6.mode !== 'dhcp') delete out.ipv6.temporaryAddresses
  else if (!out.ipv6.temporaryAddresses) delete out.ipv6.temporaryAddresses
  config.upsertInterface(out)
  emit('saved', out)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="title">
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          id="if-desc"
          label="Description"
          hint="Shown wherever this interface is picked, e.g. Office LAN."
        >
          <input id="if-desc" v-model="form.description" class="input" />
        </FormField>
        <FormField
          id="if-zone"
          label="Zone"
          hint="Firewall rules match on zones. Interfaces in the same zone share one rule list."
        >
          <select id="if-zone" v-model="form.zone" class="input">
            <option value="">Unassigned (traffic dropped)</option>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
            <option :value="NEW_ZONE">New zone…</option>
          </select>
        </FormField>
      </div>
      <template v-if="creatingZone">
        <FormField
          id="if-new-zone"
          label="New zone name"
          hint="Lowercase letters, digits, and underscores, e.g. guest or iot."
        >
          <input
            id="if-new-zone"
            v-model="newZone"
            class="input font-mono"
            autocapitalize="none"
            spellcheck="false"
            required
          />
        </FormField>
        <p v-if="zoneError" role="alert" class="text-sm text-bad">
          {{ zoneError }}
        </p>
      </template>
      <p v-else-if="zoneMates.length" class="text-sm text-ink-muted">
        Shares zone <span class="font-mono">{{ form.zone }}</span> with
        <span class="font-mono">{{ zoneMates.join(', ') }}</span
        >, so they are all matched by the same rules.
      </p>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
        Enabled
      </label>

      <p v-if="daemonOwned" class="text-sm text-ink-muted">
        The tailnet gives this interface its addresses and its MTU. Its settings are on
        <RouterLink to="/vpn/tailscale" class="underline">Tailscale</RouterLink>.
      </p>

      <p v-if="form.wireless" class="text-sm text-ink-muted">
        Its network and security are on
        <RouterLink to="/wireless" class="underline">Wireless</RouterLink>.
      </p>

      <fieldset v-if="!daemonOwned" class="space-y-3 rounded-md border border-line p-3">
        <legend class="group-title px-1">IPv4</legend>
        <FormField id="if-v4-mode" label="Mode">
          <select id="if-v4-mode" v-model="form.ipv4.mode" class="input">
            <option value="none">None</option>
            <option value="static">Static</option>
            <option value="dhcp">DHCP client</option>
          </select>
        </FormField>
        <div v-if="form.ipv4.mode === 'static'" class="grid gap-3 sm:grid-cols-2">
          <FormField id="if-v4-addr" label="Address (CIDR)">
            <input
              id="if-v4-addr"
              v-model="form.ipv4.address"
              class="input font-mono"
              placeholder="192.168.1.1/24"
              required
            />
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
        <label v-if="form.ipv4.mode === 'dhcp'" class="flex items-start gap-2 text-sm">
          <input
            id="if-v4-hostname"
            v-model="form.ipv4.sendHostname"
            type="checkbox"
            class="mt-0.5 size-4 rounded border-line-2"
          />
          <span>
            Send hostname
            <span class="block text-ink-muted">Off sends no name to the provider.</span>
          </span>
        </label>
      </fieldset>

      <fieldset v-if="!daemonOwned" class="space-y-3 rounded-md border border-line p-3">
        <legend class="group-title px-1">IPv6</legend>
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
          hint="The prefix size to request upstream. Empty asks for nothing."
        >
          <input
            id="if-v6-hint"
            v-model="form.ipv6.prefixHint"
            class="input font-mono"
            placeholder="::/56"
            spellcheck="false"
          />
        </FormField>
        <div v-if="form.ipv6.mode === 'delegated'" class="grid gap-3 sm:grid-cols-2">
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
            hint="Which /64 of that prefix this interface takes. Each one needs its own."
          >
            <input
              id="if-v6-subnet"
              v-model.number="form.ipv6.subnetId"
              type="number"
              min="0"
              max="65535"
              class="input w-32 font-mono"
            />
          </FormField>
        </div>
        <div v-if="form.ipv6.mode === 'static'" class="grid gap-3 sm:grid-cols-2">
          <FormField id="if-v6-addr" label="Address (CIDR)">
            <input
              id="if-v6-addr"
              v-model="form.ipv6.address"
              class="input font-mono"
              placeholder="2001:db8::1/64"
              required
            />
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
        <label
          v-if="form.ipv6.mode === 'slaac' || form.ipv6.mode === 'dhcp'"
          class="flex items-start gap-2 text-sm"
        >
          <input
            id="if-v6-temporary"
            v-model="form.ipv6.temporaryAddresses"
            type="checkbox"
            class="mt-0.5 size-4 rounded border-line-2"
          />
          <span>
            Temporary addresses
            <span class="block text-ink-muted">On changes the outgoing address daily.</span>
          </span>
        </label>
      </fieldset>

      <FormField v-if="!daemonOwned" id="if-mtu" label="MTU" :hint="mtuHint">
        <input
          id="if-mtu"
          v-model.number="form.mtu"
          type="number"
          min="68"
          max="65535"
          class="input w-32"
        />
      </FormField>
      <p v-if="pinsLiveMtu && !daemonOwned" class="text-sm text-ink-muted">
        {{ form.name }} is running at {{ live.mtu }}, which nothing in the configuration asks for.
        Saving this keeps it after a reboot.
      </p>

      <FormField v-if="physical" id="if-mac" label="MAC address" hint="Empty keeps the card's own.">
        <input
          id="if-mac"
          v-model="form.macAddress"
          class="input w-56 font-mono"
          placeholder="optional"
          spellcheck="false"
          autocapitalize="none"
        />
      </FormField>

      <fieldset class="space-y-3 rounded-md border border-line p-3">
        <legend class="group-title px-1">Traffic arriving here</legend>
        <FormField
          id="if-logdrops"
          label="Log packets dropped by the default policy"
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

        <label class="flex items-start gap-2 text-sm">
          <input
            v-model="form.blockPrivate"
            type="checkbox"
            class="mt-0.5 size-4 rounded border-line-2"
          />
          <span>
            Block private and loopback sources
            <span class="block text-ink-muted">
              Drops traffic arriving from 10/8, 172.16/12, 192.168/16, 127/8, and fc00::/7, but not
              link-local.
            </span>
          </span>
        </label>
        <AppNotice v-if="form.blockPrivate && privateItself">
          This interface is itself on a private network, so blocking private sources here would cut
          it off.
        </AppNotice>

        <label class="flex items-start gap-2 text-sm">
          <input
            v-model="form.blockBogons"
            type="checkbox"
            class="mt-0.5 size-4 rounded border-line-2"
          />
          <span>
            Block bogon sources
            <span class="block text-ink-muted">
              Drops traffic from prefixes nobody has been allocated. The list is refreshed daily.
            </span>
          </span>
        </label>
        <AppNotice v-if="(form.blockPrivate || form.blockBogons) && form.zone && !external">
          Zone {{ form.zone }} is internal, so blocking by source address here drops legitimate
          traffic.
        </AppNotice>
      </fieldset>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
