<script setup>
import { Upload } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import ZoneField, { NEW_ZONE } from '@/components/ZoneField.vue'
import { nextOvpnName, parseOvpn } from '@/lib/ovpnconf'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** Take only the certificates and keys, for a tunnel that exists. */
  blocksOnly: { type: Boolean, default: false },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['blocks'])

const config = useConfigStore()
const text = ref('')
const name = ref('')
const zone = ref('')
const wayOut = ref(false)
const monitor = ref('')
const username = ref('')
const password = ref('')
const error = ref('')
const fileInput = ref(null)
const zoneField = ref(null)

watch(open, (now) => {
  if (!now) return
  text.value = ''
  name.value = nextOvpnName(config.interfaces.map((i) => i.name))
  zone.value = config.zones.some((z) => z.name === name.value) ? name.value : NEW_ZONE
  wayOut.value = false
  monitor.value = ''
  username.value = ''
  password.value = ''
  error.value = ''
})

const parsed = computed(() => (text.value.trim() ? parseOvpn(text.value) : null))

/** The certificate and key problems, which are all that matter when replacing them. */
const refused = computed(() => {
  const all = parsed.value?.refused ?? []
  if (!props.blocksOnly) return all
  return all.filter((r) => r.text.startsWith('<') && r.text !== '<cert>')
})
const ok = computed(() => Boolean(parsed.value) && !refused.value.length)

watch(parsed, (p) => {
  if (!p) return
  wayOut.value = p.wayOut
  if (p.tunnel.username && !username.value) username.value = p.tunnel.username
  if (p.tunnel.password && !password.value) password.value = p.tunnel.password
})

/** The first letter up, for a line that starts a message. */
const sentence = (s) => s.charAt(0).toUpperCase() + s.slice(1)

async function choose(e) {
  const f = e.target.files?.[0]
  e.target.value = ''
  if (!f) return
  text.value = await f.text()
}

/** The blocks a tunnel is checked and known by, and nothing else. */
function blocks(t) {
  const out = {}
  for (const k of ['caPem', 'certPem', 'keyPem', 'tlsCryptKeyPem', 'tlsAuthKeyPem', 'keyDirection'])
    if (t[k] !== undefined) out[k] = t[k]
  return out
}

function save() {
  error.value = ''
  const t = parsed.value.tunnel
  if (props.blocksOnly) {
    emit('blocks', blocks(t))
    open.value = false
    return
  }
  const n = name.value.trim()
  if (!/^ovpn[0-9]+$/.test(n)) {
    error.value = 'A tunnel is named ovpn and a number, like ovpn0.'
    return
  }
  if (config.interfaces.some((i) => i.name === n)) {
    error.value = `${n} is taken. Give the tunnel another name.`
    return
  }
  const addGateway = wayOut.value && Boolean(monitor.value.trim())
  if (addGateway && [...config.gateways, ...config.gatewayGroups].some((g) => g.name === n)) {
    error.value = `A gateway or group is already called ${n}. Give the tunnel another name.`
    return
  }
  const creating = zone.value === NEW_ZONE
  if (!zoneField.value.commit()) return
  if (creating && wayOut.value) {
    const z = config.zones.find((x) => x.name === zone.value)
    if (z && !z.external) config.upsertZone({ ...z, external: true })
  }
  const openvpn = { ...t }
  delete openvpn.username
  delete openvpn.password
  if (username.value.trim()) {
    openvpn.username = username.value.trim()
    if (password.value) openvpn.password = password.value
  }
  const iface = {
    name: n,
    enabled: true,
    ipv4: { mode: 'none' },
    ipv6: { mode: 'none' },
    openvpn,
  }
  if (zone.value) iface.zone = zone.value
  config.upsertInterface(iface)
  if (addGateway) {
    config.upsertGateway({ name: n, enabled: true, interface: n, monitor: monitor.value.trim() })
  }
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="blocksOnly ? 'Replace certificates from file' : 'Add OpenVPN tunnel from file'"
    description="An OpenVPN client file with its certificates inline, as a provider or a server hands it out."
  >
    <form id="ovpn-file-form" class="space-y-4" @submit.prevent="save">
      <FormField id="ov-text" label="File" hint="Pasted, or chosen below. Nothing in it is run.">
        <textarea
          id="ov-text"
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
          id="ov-file"
          ref="fileInput"
          type="file"
          accept=".ovpn,.conf,text/plain"
          class="sr-only"
          aria-label="OpenVPN file"
          @change="choose"
        />
      </div>

      <div v-if="refused.length" role="alert">
        <ul class="space-y-1 text-sm text-bad">
          <li v-for="r in refused" :key="`${r.line}-${r.text}`">
            {{
              r.line ? `Line ${r.line}: ${r.text} ${r.reason}.` : `${sentence(r.text)} ${r.reason}.`
            }}
          </li>
        </ul>
      </div>

      <template v-if="ok && !blocksOnly">
        <dl class="kv">
          <dt>Remotes</dt>
          <dd>
            <div v-for="r in parsed.tunnel.remotes" :key="`${r.host}-${r.port}-${r.proto}`">
              <span class="font-mono">{{ r.host }}:{{ r.port }}</span>
              <span class="text-ink-muted"> · {{ r.proto || 'udp' }}</span>
            </div>
          </dd>
          <template v-if="parsed.tunnel.routes?.length">
            <dt>Routes</dt>
            <dd class="font-mono">{{ parsed.tunnel.routes.join(', ') }}</dd>
          </template>
        </dl>
        <div class="fields">
          <FormField id="ov-name" label="Interface name" hint="ovpn and a number.">
            <input
              id="ov-name"
              v-model="name"
              class="input font-mono"
              required
              spellcheck="false"
            />
          </FormField>
          <ZoneField id="ov-zone" ref="zoneField" v-model="zone" :iface="name" :suggested="name" />
        </div>
        <ToggleRow
          id="ov-wayout"
          v-model="wayOut"
          label="Use as a way out"
          hint="Rules can then send traffic out this tunnel, and nowhere else while it is down."
        />
        <FormField
          v-if="wayOut"
          id="ov-monitor"
          label="Monitor address"
          hint="An IPv4 address beyond the tunnel the probe pings. Empty: no gateway."
        >
          <input
            id="ov-monitor"
            v-model="monitor"
            class="input font-mono"
            placeholder="No gateway"
            spellcheck="false"
          />
        </FormField>
        <div v-if="parsed.needsCredentials" class="fields">
          <FormField id="ov-username" label="Username" hint="The file asks for a sign-in.">
            <input
              id="ov-username"
              v-model="username"
              class="input"
              autocomplete="off"
              spellcheck="false"
            />
          </FormField>
          <FormField id="ov-password" label="Password" hint="Kept on this router.">
            <input
              id="ov-password"
              v-model="password"
              type="password"
              class="input"
              autocomplete="new-password"
            />
          </FormField>
        </div>
      </template>
      <template v-if="parsed">
        <p v-if="parsed.dropped.length" class="text-sm text-ink-muted">
          Ignored:
          {{ parsed.dropped.map((d) => `${d.text} on line ${d.line}`).join(', ') }}.
        </p>
        <p v-for="note in parsed.notes" :key="note" class="text-sm text-ink-muted">
          {{ sentence(note) }}
        </p>
      </template>

      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="ovpn-file-form" class="btn-primary" :disabled="!ok">
        {{ blocksOnly ? 'Replace' : 'Save to draft' }}
      </button>
    </template>
  </AppDialog>
</template>
