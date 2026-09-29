<script setup>
import { Check, Copy, LoaderCircle, Trash2 } from 'lucide-vue-next'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { parseList } from '@/lib/lists'
import { encode, svgPath } from '@/lib/qr'
import { deviceAddresses, deviceFile, endpoint, isPublic, networkOf, nextFree } from '@/lib/wgconf'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

const props = defineProps({
  tunnel: { type: Object, default: null },
  peer: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
/** The tunnel a new peer goes on, when there are several to choose from. */
const chosen = ref('')
/** The peer's tunnel: its own when it has one, else the one chosen. */
const current = computed(() =>
  props.peer ? props.tunnel : (config.tunnels.find((t) => t.name === chosen.value) ?? props.tunnel),
)
const confirm = useConfirmStore()
const form = ref(blank())
const error = ref('')
const showKey = ref(false)
/** 'make' has the router make the device's keys; 'paste' takes its public key. */
const keys = ref('make')
const busy = ref(false)
/**
 * The device just given keys: its name, its private key and the addresses
 * it holds. The private key lives here until the dialog closes and is in
 * no request and no draft.
 */
const device = ref(null)
const deviceForm = ref({ host: '', route: 'networks', keepalive: 0 })
const candidates = ref([])

function blank() {
  return {
    name: '',
    description: '',
    enabled: true,
    publicKey: '',
    presharedKey: '',
    allowedIps: '',
    endpoint: '',
    keepalive: 25,
    masquerade: false,
    theirs: [],
    ours: [],
  }
}

/** A peer's maps as rows to edit, apart from the peer itself. */
const rows = (maps) => (maps ?? []).map((m) => ({ network: m.network, as: m.as }))

/** The rows filled in, as the model takes them. */
const maps = (list) =>
  list.map((m) => ({ network: m.network.trim(), as: m.as.trim() })).filter((m) => m.network && m.as)

watch(
  () => [open.value, props.peer],
  () => {
    if (!open.value) {
      device.value = null
      return
    }
    error.value = ''
    showKey.value = false
    keys.value = 'make'
    device.value = null
    chosen.value = props.tunnel?.name ?? ''
    const p = props.peer
    form.value = p
      ? {
          ...blank(),
          ...p,
          allowedIps: (p.allowedIps ?? []).join(', '),
          theirs: rows(p.theirs),
          ours: rows(p.ours),
        }
      : { ...blank(), allowedIps: suggestAddresses().join(', ') }
  },
  { immediate: true },
)

// Another tunnel means other free addresses, unless some were typed in.
watch(chosen, (now, before) => {
  if (props.peer || !open.value || !before || now === before) return
  const earlier = config.tunnels.find((t) => t.name === before)
  if (form.value.allowedIps === suggestFor(earlier).join(', '))
    form.value.allowedIps = suggestAddresses().join(', ')
})

/** A device dials in, so only a tunnel that listens can be given one. */
const listens = computed(() => Boolean(current.value?.wireguard?.listenPort))
const making = computed(() => !props.peer && listens.value && keys.value === 'make')

/** The next free address in each family the tunnel has. */
function suggestAddresses() {
  return suggestFor(current.value)
}

/** The next free address in each family a tunnel has. */
function suggestFor(t) {
  const taken = (t?.wireguard?.peers ?? []).flatMap((p) => p.allowedIps ?? [])
  return [t?.ipv4?.address, t?.ipv6?.address]
    .filter(Boolean)
    .map((a) => nextFree(a, taken))
    .filter(Boolean)
}

/** The addresses the device holds out of the ones listed for it. */
function ownAddresses(allowed) {
  const t = current.value
  return deviceAddresses(allowed, [t?.ipv4?.address, t?.ipv6?.address].filter(Boolean))
}

/** Whether the device would hold an address, saying so when it would not. */
function holdsAddress() {
  if (ownAddresses(parseList(form.value.allowedIps)).length) return true
  const suggested = suggestAddresses()[0]
  error.value = suggested
    ? `Add the device's own address, such as ${suggested}.`
    : "Add the device's own address, a /32 or a /128."
  return false
}

/** Replacing a key the peer already holds is worth a question first. */
async function generatePSK() {
  if (
    form.value.presharedKey &&
    !(await confirm.ask({
      question: `Replace the preshared key of ${form.value.name || 'this peer'}?`,
      description: 'The peer has to be given the new value.',
      confirmLabel: 'Replace',
      danger: false,
    }))
  )
    return
  try {
    const res = await api.wireguard.psk()
    form.value.presharedKey = res.presharedKey
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e)
  }
}

function peerOut() {
  const f = form.value
  const out = {
    name: f.name.trim(),
    enabled: f.enabled,
    publicKey: f.publicKey.trim(),
    allowedIps: parseList(f.allowedIps),
  }
  if (f.description) out.description = f.description
  if (f.presharedKey) out.presharedKey = f.presharedKey.trim()
  if (f.endpoint) out.endpoint = f.endpoint.trim()
  if (Number(f.keepalive)) out.keepalive = Number(f.keepalive)
  if (f.masquerade) out.masquerade = true
  const theirs = maps(f.theirs)
  if (theirs.length) out.theirs = theirs
  const ours = maps(f.ours)
  if (ours.length) out.ours = ours
  return out
}

/**
 * A key pair and a preshared key from the router. The public key and the
 * preshared key go into the peer; the private key only into the file.
 */
async function makeKeys() {
  const [pair, psk] = await Promise.all([api.wireguard.keys(), api.wireguard.psk()])
  return { privateKey: pair.privateKey, publicKey: pair.publicKey, presharedKey: psk.presharedKey }
}

async function save() {
  error.value = ''
  if (!making.value) {
    config.upsertPeer(current.value.name, peerOut(), props.peer?.name ?? form.value.name.trim())
    open.value = false
    return
  }
  if (!holdsAddress()) return
  busy.value = true
  try {
    const made = await makeKeys()
    const out = { ...peerOut(), publicKey: made.publicKey, presharedKey: made.presharedKey }
    config.upsertPeer(current.value.name, out, out.name)
    showDevice(out, made.privateKey)
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

/** New keys for a peer that has some: the old file stops working. */
async function remake() {
  const name = props.peer.name
  if (!holdsAddress()) return
  if (
    !(await confirm.ask({
      question: `Make new keys for ${name}?`,
      description: 'The device stops connecting until it is given the new file.',
      confirmLabel: 'Make new keys',
      typed: name,
    }))
  )
    return
  busy.value = true
  error.value = ''
  try {
    const made = await makeKeys()
    const out = { ...peerOut(), publicKey: made.publicKey, presharedKey: made.presharedKey }
    config.upsertPeer(current.value.name, out, name)
    showDevice(out, made.privateKey)
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

function showDevice(peer, privateKey) {
  device.value = {
    name: peer.name,
    privateKey,
    addresses: ownAddresses(peer.allowedIps),
    presharedKey: peer.presharedKey,
  }
  deviceForm.value = { host: '', route: 'networks', keepalive: 0 }
  loadCandidates()
}

/**
 * Where a device can find this router: the names dynamic DNS keeps, then
 * the public addresses the external links have now. Either list may be
 * empty, and the field takes anything typed.
 */
async function loadCandidates() {
  const names = []
  const addresses = []
  const [ddns, overview] = await Promise.allSettled([api.ddns.status(), api.overview()])
  if (ddns.status === 'fulfilled') {
    for (const r of ddns.value.records ?? [])
      if (r.name && !names.includes(r.name)) names.push(r.name)
  }
  if (overview.status === 'fulfilled') {
    for (const link of overview.value.interfaces ?? []) {
      if (!link.external) continue
      for (const a of link.addresses ?? []) {
        const addr = a.split('/')[0]
        if (isPublic(addr) && !addresses.includes(addr)) addresses.push(addr)
      }
    }
  }
  candidates.value = [...names, ...addresses]
  if (!deviceForm.value.host) deviceForm.value.host = candidates.value[0] ?? ''
}

/** The zones a device's traffic must not be sent to by default: the WAN side. */
const external = computed(() => new Set(config.zones.filter((z) => z.external).map((z) => z.name)))

/** This router's inside networks and the tunnel's own. */
const routerNetworks = computed(() => {
  const out = []
  for (const i of config.interfaces) {
    const inside =
      i.name === current.value?.name || (i.enabled && i.zone && !external.value.has(i.zone))
    if (!inside) continue
    for (const fam of [i.ipv4, i.ipv6]) {
      if (fam?.mode !== 'static' || !fam.address) continue
      const net = networkOf(fam.address)
      if (net && !out.includes(net)) out.push(net)
    }
  }
  return out
})

/**
 * The tunnel's addresses, when this router answers DNS on it: the DNS
 * page's interfaces, or with none named every one outside an external zone.
 */
const dnsAddresses = computed(() => {
  const t = current.value
  const dns = config.draft?.services?.dns
  if (!t || !dns?.enabled) return []
  const named = dns.interfaces ?? []
  const answers = named.length ? named.includes(t.name) : !external.value.has(t.zone)
  if (!answers) return []
  return [t.ipv4?.address, t.ipv6?.address].filter(Boolean).map((a) => a.split('/')[0])
})

const fileText = computed(() => {
  const d = device.value
  if (!d) return ''
  const f = deviceForm.value
  return deviceFile({
    privateKey: d.privateKey,
    addresses: d.addresses,
    dns: dnsAddresses.value,
    peer: {
      publicKey: current.value?.wireguard?.publicKey ?? '',
      presharedKey: d.presharedKey,
      allowedIps: f.route === 'everything' ? ['0.0.0.0/0', '::/0'] : routerNetworks.value,
      endpoint: endpoint(f.host, current.value?.wireguard?.listenPort),
      keepalive: Number(f.keepalive) || 0,
    },
  })
})

const qr = computed(() => (fileText.value ? encode(fileText.value) : null))
const qrPath = computed(() => (qr.value ? svgPath(qr.value) : ''))
const fileName = computed(() => `${current.value?.name}-${device.value?.name}.conf`)

function download() {
  const url = URL.createObjectURL(new Blob([fileText.value], { type: 'text/plain' }))
  const a = document.createElement('a')
  a.href = url
  a.download = fileName.value
  a.click()
  URL.revokeObjectURL(url)
}

const fileEl = ref(null)
/** 'copied', 'selected' when only the fallback's selection worked, or ''. */
const copied = ref('')
let copyTimer = 0
onBeforeUnmount(() => window.clearTimeout(copyTimer))

/**
 * The clipboard API needs HTTPS or localhost, which a router reached over
 * plain HTTP on its LAN is not, so that falls back to selecting the file
 * and the older copy command. Either way the file ends up selected.
 */
async function copyFile() {
  const range = document.createRange()
  range.selectNodeContents(fileEl.value)
  const selection = window.getSelection()
  selection.removeAllRanges()
  selection.addRange(range)
  let ok
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(fileText.value)
      ok = true
    } else ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  copied.value = ok ? 'copied' : 'selected'
  window.clearTimeout(copyTimer)
  copyTimer = window.setTimeout(() => (copied.value = ''), 2000)
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="device ? `File for ${device.name}` : peer ? `Peer ${peer.name}` : 'Add peer'"
    :description="`On ${current?.name ?? 'the tunnel'}.`"
  >
    <div v-if="device" class="space-y-4">
      <p class="text-sm">
        The private key is shown only now. Close this and it is gone. Make new keys replaces it.
      </p>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          id="dv-endpoint"
          label="Endpoint"
          :hint="`With the tunnel's port, ${current?.wireguard?.listenPort}.`"
        >
          <input
            id="dv-endpoint"
            v-model="deviceForm.host"
            list="dv-endpoints"
            class="input font-mono"
            spellcheck="false"
          />
          <datalist id="dv-endpoints">
            <option v-for="c in candidates" :key="c" :value="c" />
          </datalist>
        </FormField>
        <FormField id="dv-route" label="Send through the tunnel">
          <select id="dv-route" v-model="deviceForm.route" class="input">
            <option value="networks">This router's networks</option>
            <option value="everything">Everything</option>
          </select>
        </FormField>
        <FormField id="dv-keep" label="Keepalive" hint="In seconds. 0 sends none.">
          <input
            id="dv-keep"
            v-model="deviceForm.keepalive"
            type="number"
            min="0"
            max="65535"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
      </div>
      <div class="flex flex-wrap items-start gap-4">
        <!-- Black on white in either theme: a scanner reads the code, not the page. -->
        <svg
          v-if="qr"
          role="img"
          :aria-label="`QR code of the file for ${device.name}`"
          :viewBox="`0 0 ${qr.size + 8} ${qr.size + 8}`"
          class="size-64 shrink-0 rounded-md bg-white max-sm:size-full"
          shape-rendering="crispEdges"
        >
          <path :d="qrPath" fill="#000" />
        </svg>
        <pre
          ref="fileEl"
          class="min-w-0 flex-1 overflow-x-auto rounded-md bg-surface-2 p-3 font-mono text-code whitespace-pre select-all"
          >{{ fileText }}</pre>
      </div>
      <span class="sr-only" aria-live="polite">{{
        copied === 'copied' ? 'Copied.' : copied === 'selected' ? 'Selected, copy it by hand.' : ''
      }}</span>
      <div class="flex flex-wrap justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="copyFile">
          <Check v-if="copied === 'copied'" class="size-4" aria-hidden="true" />
          <Copy v-else class="size-4" aria-hidden="true" />
          {{ copied === 'copied' ? 'Copied' : copied === 'selected' ? 'Selected' : 'Copy' }}
        </button>
        <button type="button" class="btn-secondary" @click="download">Download</button>
        <button type="button" class="btn-primary" @click="open = false">Done</button>
      </div>
    </div>

    <form v-else class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="pe-name" label="Name" hint="Lower case, e.g. laptop.">
          <input
            id="pe-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="pe-desc" label="Description">
          <input id="pe-desc" v-model="form.description" class="input" />
        </FormField>
      </div>
      <div v-if="!peer && (config.tunnels.length > 1 || listens)" class="grid gap-4 sm:grid-cols-2">
        <FormField v-if="config.tunnels.length > 1" id="pe-tunnel" label="Tunnel">
          <select id="pe-tunnel" v-model="chosen" class="input font-mono">
            <option v-for="t in config.tunnels" :key="t.name" :value="t.name">
              {{ t.description ? `${t.name} (${t.description})` : t.name }}
            </option>
          </select>
        </FormField>
        <FormField
          v-if="listens"
          id="pe-keys"
          label="Keys"
          :hint="making ? 'The file is shown once. Only the public key is kept.' : ''"
        >
          <select id="pe-keys" v-model="keys" class="input">
            <option value="make">Make them here</option>
            <option value="paste">Paste its public key</option>
          </select>
        </FormField>
      </div>
      <FormField v-if="!making" id="pe-pub" label="Public key">
        <input
          id="pe-pub"
          v-model="form.publicKey"
          class="input font-mono"
          required
          spellcheck="false"
        />
      </FormField>
      <FormField
        id="pe-allowed"
        label="Allowed addresses"
        hint="Its tunnel address, and the networks behind it. Comma separated."
      >
        <input
          id="pe-allowed"
          v-model="form.allowedIps"
          class="input font-mono"
          required
          spellcheck="false"
        />
      </FormField>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          id="pe-endpoint"
          label="Endpoint"
          hint="host:port or [IPv6]:port. Empty for a peer that calls in."
        >
          <input
            id="pe-endpoint"
            v-model="form.endpoint"
            class="input font-mono"
            spellcheck="false"
          />
        </FormField>
        <FormField id="pe-keep" label="Keepalive" hint="In seconds. 25 keeps a NAT binding open.">
          <input
            id="pe-keep"
            v-model="form.keepalive"
            type="number"
            min="0"
            max="65535"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
      </div>
      <template v-if="!making">
        <FormField
          id="pe-psk"
          label="Preshared key"
          hint="Optional. The peer needs the same value."
        >
          <div class="flex gap-2">
            <input
              id="pe-psk"
              v-model="form.presharedKey"
              :type="showKey ? 'text' : 'password'"
              class="input font-mono"
              autocomplete="off"
              spellcheck="false"
            />
            <button type="button" class="btn-secondary" @click="showKey = !showKey">
              {{ showKey ? 'Hide' : 'Show' }}
            </button>
          </div>
        </FormField>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn-secondary" @click="generatePSK">
            Generate preshared key
          </button>
          <button
            v-if="peer && listens"
            type="button"
            class="btn-secondary"
            :disabled="busy"
            @click="remake"
          >
            Make new keys
          </button>
        </div>
      </template>

      <ToggleRow
        v-model="form.masquerade"
        label="Translate to the tunnel address"
        hint="For a far end that does not route your networks. It cannot open connections to them."
      />

      <section class="space-y-2">
        <div class="flex items-center gap-3">
          <h3 class="group-title">Show their networks here as</h3>
          <button
            type="button"
            class="btn-secondary"
            @click="form.theirs.push({ network: '', as: '' })"
          >
            Add network
          </button>
        </div>
        <p v-if="!form.theirs.length" class="text-sm text-ink-muted">
          None. For a far end numbered like this side, each host keeping its number.
        </p>
        <ul class="space-y-2">
          <li v-for="(m, i) in form.theirs" :key="i" class="form-row flex-nowrap">
            <FormField :id="`pe-theirs-${i}`" label="Their network" class="flex-1">
              <input
                :id="`pe-theirs-${i}`"
                v-model="m.network"
                class="input font-mono"
                spellcheck="false"
              />
            </FormField>
            <FormField :id="`pe-theirs-as-${i}`" label="Shown as" class="flex-1">
              <input
                :id="`pe-theirs-as-${i}`"
                v-model="m.as"
                class="input font-mono"
                spellcheck="false"
              />
            </FormField>
            <button
              type="button"
              class="link inline-flex items-center text-bad"
              :aria-label="`Remove ${m.network || 'this network'}`"
              @click="form.theirs.splice(i, 1)"
            >
              <Trash2 class="size-4" aria-hidden="true" />
            </button>
          </li>
        </ul>
      </section>

      <section class="space-y-2">
        <div class="flex items-center gap-3">
          <h3 class="group-title">Show this side's networks there as</h3>
          <button
            type="button"
            class="btn-secondary"
            @click="form.ours.push({ network: '', as: '' })"
          >
            Add network
          </button>
        </div>
        <p v-if="!form.ours.length" class="text-sm text-ink-muted">None.</p>
        <ul class="space-y-2">
          <li v-for="(m, i) in form.ours" :key="i" class="form-row flex-nowrap">
            <FormField :id="`pe-ours-${i}`" label="This side's network" class="flex-1">
              <input
                :id="`pe-ours-${i}`"
                v-model="m.network"
                class="input font-mono"
                spellcheck="false"
              />
            </FormField>
            <FormField :id="`pe-ours-as-${i}`" label="Shown as" class="flex-1">
              <input
                :id="`pe-ours-as-${i}`"
                v-model="m.as"
                class="input font-mono"
                spellcheck="false"
              />
            </FormField>
            <button
              type="button"
              class="link inline-flex items-center text-bad"
              :aria-label="`Remove ${m.network || 'this network'}`"
              @click="form.ours.splice(i, 1)"
            >
              <Trash2 class="size-4" aria-hidden="true" />
            </button>
          </li>
        </ul>
      </section>
      <ToggleRow v-model="form.enabled" label="Enabled" />
      <p v-if="error" role="alert" class="text-sm text-bad">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="busy" :aria-busy="busy">
          <LoaderCircle v-if="busy" class="size-4 animate-spin" aria-hidden="true" />
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
