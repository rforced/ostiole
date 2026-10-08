<script setup>
import { Upload } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
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
const fileInput = ref(null)

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
const ok = computed(() => Boolean(parsed.value && !parsed.value.errors.length))
/** A peer that takes a default route makes the tunnel a way out. */
const wayOut = computed(
  () => ok.value && parsed.value.peers.some((p) => takesDefaultRoute(p.allowedIps)),
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
  if (!ok.value) return []
  const base = peerName(fileName.value)
  return parsed.value.peers.map((p, i) => {
    const out = {
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
const sentence = (s) => s.charAt(0).toUpperCase() + s.slice(1)

async function choose(e) {
  const f = e.target.files?.[0]
  e.target.value = ''
  if (!f) return
  text.value = await f.text()
  fileName.value = f.name
  if (!description.value) description.value = f.name.replace(/\.conf$/i, '')
}

/** @param {string} cidr */
const address = (cidr) => (cidr ? { mode: 'static', address: cidr } : { mode: 'none' })

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
  busy.value = true
  try {
    const { publicKey } = await api.wireguard.publicKey(p.iface.privateKey)
    if (!zoneExists.value) {
      const zone = { name: n }
      if (wayOut.value) zone.external = true
      config.upsertZone(zone)
    }
    const iface = {
      name: n,
      enabled: true,
      zone: n,
      ipv4: address(p.iface.ipv4),
      ipv6: address(p.iface.ipv6),
      wireguard: { privateKey: p.iface.privateKey, publicKey, peers: peers.value },
    }
    if (description.value.trim()) iface.description = description.value.trim()
    if (p.iface.mtu) iface.mtu = p.iface.mtu
    if (p.iface.listenPort) iface.wireguard.listenPort = p.iface.listenPort
    config.upsertInterface(iface)
    if (addGateway) {
      config.upsertGateway({
        name: n,
        enabled: true,
        interface: n,
        priority: 0,
        monitor: monitor.value.trim(),
      })
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
    <form class="space-y-4" @submit.prevent="save">
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

      <ul v-if="parsed?.errors.length" role="alert" class="space-y-1 text-sm text-bad">
        <li v-for="e in parsed.errors" :key="`${e.line}-${e.message}`">
          {{ e.line ? `Line ${e.line}: ${e.message}.` : `${sentence(e.message)}.` }}
        </li>
      </ul>

      <template v-if="ok">
        <div class="grid gap-4 sm:grid-cols-2">
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
            {{ [parsed.iface.ipv4, parsed.iface.ipv6].filter(Boolean).join(', ') || '—' }}
          </dd>
          <dt>Listening on</dt>
          <dd class="font-mono">
            {{ parsed.iface.listenPort ? `udp/${parsed.iface.listenPort}` : 'only dials out' }}
          </dd>
          <template v-if="parsed.iface.mtu">
            <dt>MTU</dt>
            <dd class="font-mono">{{ parsed.iface.mtu }}</dd>
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
                · {{ p.endpoint || 'calls in' }} · {{ p.allowedIps.join(', ') }}</span
              >
            </div>
          </dd>
        </dl>
        <p v-if="parsed.ignored.length" class="text-sm text-ink-muted">
          Ignored:
          {{ parsed.ignored.map((i) => `${i.key} on line ${i.line}`).join(', ') }}.
        </p>
        <p v-for="note in parsed.notes" :key="note" class="text-sm text-ink-muted">{{ note }}</p>
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
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <ActionButton
          type="submit"
          kind="primary"
          label="Save to draft"
          busy-label="Saving…"
          :busy="busy"
          :disabled="!ok"
        />
      </div>
    </form>
  </AppDialog>
</template>
