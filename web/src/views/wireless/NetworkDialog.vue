<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The interface being edited, or null for a new network. */
  network: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())
const showPass = ref(false)

const SECURITY = [
  { value: 'wpa2-wpa3', label: 'WPA2 and WPA3', hint: 'WPA3 where the client has it.' },
  { value: 'wpa3', label: 'WPA3', hint: 'Older devices cannot join.' },
  { value: 'wpa2', label: 'WPA2', hint: '' },
  { value: 'owe', label: 'Enhanced open', hint: 'No password. Traffic is still encrypted.' },
  { value: 'open', label: 'Open', hint: 'No password. Traffic is not encrypted.' },
]

function blank() {
  return {
    name: '',
    previousName: '',
    enabled: true,
    radio: '',
    ssid: '',
    security: 'wpa2-wpa3',
    passphrase: '',
    hidden: false,
    isolate: false,
    maxClients: 0,
    attach: 'bridge',
    bridge: '',
    zone: '',
    address: '',
  }
}

const bridges = computed(() => config.interfaces.filter((i) => i.bridge))

watch(
  () => [open.value, props.network],
  () => {
    if (!open.value) return
    showPass.value = false
    const n = props.network
    if (n) {
      const master = bridges.value.find((b) => b.bridge.members?.includes(n.name))
      form.value = {
        ...blank(),
        name: n.name,
        previousName: n.name,
        enabled: n.enabled,
        radio: n.wireless.radio,
        ssid: n.wireless.ssid,
        security: n.wireless.security,
        passphrase: n.wireless.passphrase ?? '',
        hidden: Boolean(n.wireless.hidden),
        isolate: Boolean(n.wireless.isolate),
        maxClients: n.wireless.maxClients ?? 0,
        attach: master ? 'bridge' : 'zone',
        bridge: master?.name ?? '',
        zone: n.zone ?? '',
        address: n.ipv4?.address ?? '',
      }
      return
    }
    form.value = blank()
    form.value.name = nextName()
    form.value.radio = config.radios[0]?.name ?? ''
    form.value.bridge = bridges.value[0]?.name ?? ''
    if (!form.value.bridge) form.value.attach = 'zone'
    form.value.zone = config.zones.find((z) => !z.external)?.name ?? ''
  },
  { immediate: true },
)

/** ap0, ap1, … whichever is free. */
function nextName() {
  const taken = new Set(config.interfaces.map((i) => i.name))
  for (let i = 0; i < 100; i += 1) if (!taken.has(`ap${i}`)) return `ap${i}`
  return 'ap0'
}

const needsPassphrase = computed(() => ['wpa2-wpa3', 'wpa3', 'wpa2'].includes(form.value.security))

const securityHint = computed(
  () => SECURITY.find((s) => s.value === form.value.security)?.hint ?? '',
)

function save() {
  const f = form.value
  const name = f.name.trim()
  const bridged = f.attach === 'bridge' && f.bridge
  const iface = {
    name,
    enabled: f.enabled,
    zone: bridged ? '' : f.zone,
    ipv4: !bridged && f.address ? { mode: 'static', address: f.address.trim() } : { mode: 'none' },
    ipv6: { mode: 'none' },
    wireless: {
      radio: f.radio,
      ssid: f.ssid.trim(),
      security: f.security,
      passphrase: needsPassphrase.value ? f.passphrase : '',
      hidden: f.hidden,
      isolate: f.isolate,
      maxClients: Number(f.maxClients) || 0,
    },
  }
  // A renamed network leaves its old interface, and its old bridge
  // membership, behind.
  if (f.previousName && f.previousName !== name) config.removeInterface(f.previousName)
  config.upsertInterface(iface)
  for (const b of bridges.value) {
    const members = b.bridge.members ?? []
    const want = bridged && b.name === f.bridge
    if (want && !members.includes(name)) {
      config.upsertInterface({ ...b, bridge: { ...b.bridge, members: [...members, name] } })
    } else if (!want && members.includes(name)) {
      config.upsertInterface({
        ...b,
        bridge: { ...b.bridge, members: members.filter((m) => m !== name) },
      })
    }
  }
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="network ? `Network ${network.wireless.ssid}` : 'Add wireless network'"
    description="A network is an interface: put it on a bridge to join an existing segment, or in a zone of its own."
  >
    <form class="space-y-4" @submit.prevent="save">
      <p v-if="!config.radios.length" class="text-sm text-ink-muted">Configure a radio first.</p>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="net-radio" label="Radio">
          <select id="net-radio" v-model="form.radio" class="input font-mono" required>
            <option v-for="r in config.radios" :key="r.name" :value="r.name">{{ r.name }}</option>
          </select>
        </FormField>
        <FormField id="net-ssid" label="Name">
          <input id="net-ssid" v-model="form.ssid" class="input" required maxlength="32" />
        </FormField>
        <FormField id="net-security" label="Security" :hint="securityHint">
          <select id="net-security" v-model="form.security" class="input">
            <option v-for="s in SECURITY" :key="s.value" :value="s.value">{{ s.label }}</option>
          </select>
        </FormField>
        <FormField
          v-if="needsPassphrase"
          id="net-pass"
          label="Passphrase"
          hint="8 to 63 characters."
        >
          <div class="flex gap-2">
            <input
              id="net-pass"
              v-model="form.passphrase"
              :type="showPass ? 'text' : 'password'"
              class="input font-mono"
              required
              minlength="8"
              maxlength="63"
              autocomplete="new-password"
            />
            <button type="button" class="btn-secondary" @click="showPass = !showPass">
              {{ showPass ? 'Hide' : 'Show' }}
            </button>
          </div>
        </FormField>
        <FormField id="net-name" label="Interface" hint="ap0, ap1, … by default.">
          <input
            id="net-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="net-max" label="Max clients" hint="0 leaves it to the daemon.">
          <input
            id="net-max"
            v-model.number="form.maxClients"
            type="number"
            min="0"
            max="2007"
            class="input w-32 font-mono"
          />
        </FormField>
      </div>

      <fieldset class="space-y-2">
        <legend class="group-title">Attach</legend>
        <label class="flex items-center gap-2 text-sm">
          <input
            v-model="form.attach"
            type="radio"
            value="bridge"
            class="size-4"
            :disabled="!bridges.length"
          />
          Bridge
          <select
            v-model="form.bridge"
            class="input w-48 font-mono"
            :disabled="form.attach !== 'bridge'"
          >
            <option v-for="b in bridges" :key="b.name" :value="b.name">{{ b.name }}</option>
          </select>
          <span v-if="!bridges.length" class="text-ink-muted">
            None yet. To share a wired LAN, bridge it first.
          </span>
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.attach" type="radio" value="zone" class="size-4" />
          Zone
          <select
            v-model="form.zone"
            class="input w-48 font-mono"
            :disabled="form.attach !== 'zone'"
          >
            <option value="">Unassigned</option>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </label>
        <FormField
          v-if="form.attach === 'zone'"
          id="net-address"
          label="Address"
          hint="The router's address here, in CIDR, on a network of its own."
        >
          <input
            id="net-address"
            v-model="form.address"
            class="input font-mono"
            spellcheck="false"
          />
        </FormField>
      </fieldset>

      <div class="space-y-2">
        <ToggleRow v-model="form.hidden" label="Hidden" />
        <ToggleRow
          v-model="form.isolate"
          label="Isolate clients"
          hint="Off lets clients see each other."
        />
        <ToggleRow v-model="form.enabled" label="Enabled" />
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.radio">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
