<script setup lang="ts">
import { Upload } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import type { Interface, WireGuard, WireGuardPeer, Zone } from '@/lib/types.gen'
import { parseQuick, peerName, takesDefaultRoute } from '@/lib/wgconf'
import { useConfigStore } from '@/stores/config'

const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const text = ref('')
const fileName = ref('')
const name = ref('')
const description = ref('')
const gateway = ref(true)
const monitor = ref('')
const error = ref('')
const busy = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

watch(open, (now) => {
  if (!now) return
  text.value = ''
  fileName.value = ''
  name.value = nextName()
  description.value = ''
  gateway.value = true
  monitor.value = ''
  error.value = ''
})

/** wg0, wg1, … whichever is free. */
function nextName() {
  const taken = new Set(config.interfaces.map((i) => i.name))
  for (let i = 0; i < 100; i += 1) if (!taken.has(`wg${i}`)) return `wg${i}`
  return 'wg0'
}

const parsed = computed(() => (text.value.trim() ? parseQuick(text.value) : null))
const file = computed(() => (parsed.value && !parsed.value.errors.length ? parsed.value : null))
const ok = computed(() => file.value !== null)
/** A peer that takes a default route makes the tunnel a way out. */
const wayOut = computed(
  () => file.value?.peers.some((p) => takesDefaultRoute(p.allowedIps)) ?? false,
)
const zoneExists = computed(() => config.zones.some((z) => z.name === name.value))

// The file's own DNS server answers inside the provider's network, so a
// probe to it tells nobody else anything.
watch(parsed, (p) => {
  if (!p || monitor.value) return
  monitor.value = p.iface.dns.find((d) => !d.includes(':')) ?? ''
})

/** The peers as they will be saved, named after the file. */
const peers = computed(() => {
  const quick = file.value
  if (!quick) return []
  const base = peerName(fileName.value)
  return quick.peers.map((p, i) => {
    const out: WireGuardPeer = {
      name: i ? `${base.slice(0, 28)}_${i + 1}` : base,
      enabled: true,
      publicKey: p.publicKey,
      allowedIps: p.allowedIps,
    }
    if (p.presharedKey) out.presharedKey = p.presharedKey
    if (p.endpoint) out.endpoint = p.endpoint
    if (p.keepalive) out.keepalive = p.keepalive
    return out
  })
})

/** The first letter up, for a line that starts a message. */
const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

async function choose(e: Event) {
  const input = e.target as HTMLInputElement
  const f = input.files?.[0]
  input.value = ''
  if (!f) return
  text.value = await f.text()
  fileName.value = f.name
  if (!description.value) description.value = f.name.replace(/\.conf$/i, '')
}

const address = (cidr: string): { mode: 'static' | 'none'; address?: string } =>
  cidr ? { mode: 'static', address: cidr } : { mode: 'none' }

async function save() {
  error.value = ''
  const n = name.value.trim()
  if (config.interfaces.some((i) => i.name === n)) {
    error.value = `${n} is taken. Give the tunnel another name.`
    return
  }
  const addGateway = wayOut.value && gateway.value
  if (addGateway && [...config.gateways, ...config.gatewayGroups].some((g) => g.name === n)) {
    error.value = `A gateway or group is already called ${n}. Give the tunnel another name.`
    return
  }
  const p = parsed.value
  if (!p) return
  busy.value = true
  try {
    const { publicKey } = (await api.wireguard.publicKey(p.iface.privateKey)) as {
      publicKey: string
    }
    if (!zoneExists.value) {
      const zone: Zone = { name: n }
      if (wayOut.value) zone.external = true
      config.upsertZone(zone)
    }
    const wireguard: WireGuard = { privateKey: p.iface.privateKey, publicKey, peers: peers.value }
    if (p.iface.listenPort) wireguard.listenPort = p.iface.listenPort
    const iface: Interface = {
      name: n,
      enabled: true,
      zone: n,
      ipv4: address(p.iface.ipv4),
      ipv6: address(p.iface.ipv6),
      wireguard,
    }
    if (description.value.trim()) iface.description = description.value.trim()
    if (p.iface.mtu) iface.mtu = p.iface.mtu
    config.upsertInterface(iface)
    if (addGateway) {
      config.upsertGateway({ name: n, enabled: true, interface: n, monitor: monitor.value.trim() })
    }
    open.value = false
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    title="Add tunnel from file"
    description="A wg-quick file, as a provider or another router hands it out."
  >
    <form id="tunnel-file-form" class="space-y-4" @submit.prevent="save">
      <FormField id="tf-text" label="File" hint="Pasted, or chosen below. Nothing in it is run.">
        <textarea
          id="tf-text"
          v-model="text"
          rows="8"
          class="input font-mono text-code"
          spellcheck="false"
          autocomplete="off"
        ></textarea>
      </FormField>
      <div>
        <button type="button" class="btn-secondary" @click="fileInput?.click()">
          <Upload class="size-4" aria-hidden="true" /> Choose file
        </button>
        <input
          ref="fileInput"
          type="file"
          accept=".conf,text/plain"
          class="sr-only"
          aria-label="WireGuard file"
          @change="choose"
        />
      </div>

      <div v-if="parsed?.errors.length" role="alert">
        <ul class="space-y-1 text-sm text-bad">
          <li v-for="e in parsed.errors" :key="`${e.line}-${e.message}`">
            {{ e.line ? `Line ${e.line}: ${e.message}.` : `${sentence(e.message)}.` }}
          </li>
        </ul>
      </div>

      <template v-if="file">
        <div class="fields">
          <FormField id="tf-name" label="Interface name">
            <input
              id="tf-name"
              v-model="name"
              class="input font-mono"
              required
              spellcheck="false"
            />
          </FormField>
          <FormField id="tf-desc" label="Description">
            <input id="tf-desc" v-model="description" class="input" />
          </FormField>
        </div>
        <dl class="kv">
          <dt>Addresses</dt>
          <dd class="font-mono">
            {{ [file.iface.ipv4, file.iface.ipv6].filter(Boolean).join(', ') || '—' }}
          </dd>
          <dt>Listening on</dt>
          <dd class="font-mono">
            {{ file.iface.listenPort ? `udp/${file.iface.listenPort}` : 'only dials out' }}
          </dd>
          <template v-if="file.iface.mtu">
            <dt>MTU</dt>
            <dd class="font-mono">{{ file.iface.mtu }}</dd>
          </template>
          <dt>Zone</dt>
          <dd>
            <span class="font-mono">{{ name }}</span
            >{{ zoneExists ? ', as it is' : wayOut ? ', new and external' : ', new' }}
          </dd>
          <dt>Peers</dt>
          <dd>
            <div v-for="p in peers" :key="p.name" class="font-mono">
              {{ p.name
              }}<span class="text-ink-muted">
                · {{ p.endpoint || 'calls in' }} · {{ (p.allowedIps ?? []).join(', ') }}</span
              >
            </div>
          </dd>
        </dl>
        <p v-if="file.ignored.length" class="text-sm text-ink-muted">
          Ignored:
          {{ file.ignored.map((i) => `${i.key} on line ${i.line}`).join(', ') }}.
        </p>
        <p v-for="note in file.notes" :key="note" class="text-sm text-ink-muted">{{ note }}</p>
        <template v-if="wayOut">
          <ToggleRow
            v-model="gateway"
            label="Add a gateway through it"
            hint="Rules can then send traffic out this tunnel, and nowhere else while it is down."
          />
          <FormField
            v-if="gateway"
            id="tf-monitor"
            label="Monitor address"
            hint="An IPv4 address beyond the tunnel. The provider's DNS server keeps the probe inside its network."
          >
            <input
              id="tf-monitor"
              v-model="monitor"
              class="input font-mono"
              required
              spellcheck="false"
            />
          </FormField>
        </template>
      </template>

      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <ActionButton
        type="submit"
        form="tunnel-file-form"
        kind="primary"
        label="Save to draft"
        busy-label="Saving…"
        :busy="busy"
        :disabled="!ok"
      />
    </template>
  </AppDialog>
</template>
