<script setup>
import { computed } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

/** The one name a Tailscale interface may have; every tool assumes it. */
const DEVICE = 'tailscale0'
/** The port peers dial by default. */
const DEFAULT_PORT = 41641

const config = useConfigStore()
const node = computed(() => config.tailscale)
const ts = computed(() => node.value?.tailscale ?? null)

const internalZones = computed(() => config.zones.filter((z) => !z.external))

function join() {
  config.upsertInterface({
    name: DEVICE,
    enabled: true,
    zone: internalZones.value[0]?.name ?? config.zones[0]?.name ?? '',
    ipv4: { mode: 'none' },
    ipv6: { mode: 'none' },
    tailscale: { port: DEFAULT_PORT },
  })
}

/** A field the model leaves out when it is empty. */
function optional(key) {
  return computed({
    get: () => ts.value?.[key] ?? '',
    set: (v) => {
      if (v) ts.value[key] = v
      else delete ts.value[key]
    },
  })
}

const hostname = optional('hostname')
const loginServer = optional('loginServer')

const port = computed({
  get: () => ts.value?.port ?? 0,
  set: (v) => {
    const n = Number(v)
    if (n) ts.value.port = n
    else delete ts.value.port
  },
})

const routes = computed({
  get: () => (ts.value?.advertiseRoutes ?? []).join(', '),
  set: (v) => {
    const list = v
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    if (list.length) ts.value.advertiseRoutes = list
    else delete ts.value.advertiseRoutes
  },
})

/** The IPv4 networks a zone's interfaces hold, for the button per zone. */
function zonePrefixes(zone) {
  return config.interfaces
    .filter((i) => i.zone === zone && i.ipv4?.mode === 'static' && i.ipv4.address)
    .map((i) => maskPrefix(i.ipv4.address))
    .filter(Boolean)
}

/** 192.168.1.1/24 is written down as the network it is on. */
function maskPrefix(address) {
  const [addr, bits] = address.split('/')
  const width = Number(bits)
  const parts = addr.split('.').map(Number)
  if (parts.length !== 4 || !Number.isInteger(width) || width < 0 || width > 32) return ''
  const value = ((parts[0] << 24) | (parts[1] << 16) | (parts[2] << 8) | parts[3]) >>> 0
  const mask = width === 0 ? 0 : (0xffffffff << (32 - width)) >>> 0
  const net = value & mask
  return `${(net >>> 24) & 255}.${(net >>> 16) & 255}.${(net >>> 8) & 255}.${net & 255}/${width}`
}

function addZone(zone) {
  const list = new Set(ts.value.advertiseRoutes ?? [])
  for (const p of zonePrefixes(zone)) list.add(p)
  ts.value.advertiseRoutes = [...list]
}
</script>

<template>
  <div v-if="!node" class="space-y-3">
    <p class="text-sm text-neutral-500">This router is not on a tailnet.</p>
    <button type="button" class="btn-secondary" @click="join">Join a tailnet</button>
  </div>

  <div v-else class="space-y-6">
    <label class="flex items-center gap-2 text-sm">
      <input v-model="node.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
      <span class="font-medium">Enabled</span>
    </label>

    <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
      <FormField id="ts-zone" label="Zone" hint="Nothing is forwarded until this zone has a rule.">
        <select id="ts-zone" v-model="node.zone" class="input font-mono">
          <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
        </select>
      </FormField>
      <FormField id="ts-port" label="Port" hint="41641. 0 opens nothing.">
        <input id="ts-port" v-model.number="port" type="number" min="0" max="65535" class="input" />
      </FormField>
      <FormField id="ts-hostname" label="Hostname" hint="The router's name.">
        <input id="ts-hostname" v-model="hostname" type="text" class="input font-mono" />
      </FormField>
      <FormField
        id="ts-login-server"
        label="Login server"
        hint="Tailscale's. Changing it needs a log out."
      >
        <input id="ts-login-server" v-model="loginServer" type="text" class="input font-mono" />
      </FormField>
    </div>

    <FormField
      id="ts-routes"
      label="Advertise routes"
      hint="Comma separated. Needs a rule from the zone above."
    >
      <input id="ts-routes" v-model="routes" type="text" class="input font-mono" />
    </FormField>
    <div class="flex flex-wrap gap-2">
      <button
        v-for="z in internalZones"
        :key="z.name"
        type="button"
        class="btn-secondary"
        :disabled="!zonePrefixes(z.name).length"
        @click="addZone(z.name)"
      >
        Add {{ z.name }}'s networks
      </button>
    </div>

    <fieldset class="space-y-2 text-sm">
      <label class="flex items-center gap-2">
        <input
          v-model="ts.advertiseExitNode"
          type="checkbox"
          class="size-4 rounded border-neutral-300"
        />
        Advertise as exit node
        <span class="text-neutral-500">
          Tailnet devices reach the internet through this router. Needs a rule from its zone to an
          external zone.
        </span>
      </label>
      <label class="flex items-center gap-2">
        <input
          v-model="ts.acceptRoutes"
          type="checkbox"
          class="size-4 rounded border-neutral-300"
        />
        Accept routes
        <span class="text-neutral-500">Other nodes' networks become routes on this router.</span>
      </label>
      <label class="flex items-center gap-2">
        <input v-model="ts.logUploads" type="checkbox" class="size-4 rounded border-neutral-300" />
        Log uploads
        <span class="text-neutral-500">
          Off keeps the daemon's logs on this router. Tailscale support cannot help without them.
        </span>
      </label>
    </fieldset>

    <p class="text-sm text-neutral-500">
      Interface <span class="font-mono">{{ node.name }}</span> in zone
      <span class="font-mono">{{ node.zone || 'unassigned' }}</span> ·
      <RouterLink to="/interfaces" class="underline">Interfaces</RouterLink>
    </p>

    <ConfirmButton
      label="Remove"
      :question="`Remove ${node.name} from the configuration?`"
      description="The daemon stops on the next apply. The node stays in the admin console."
      :dependents="config.interfaceDependents(node.name)"
      :typed="node.name"
      @confirm="config.removeInterface(node.name)"
    />
  </div>
</template>
