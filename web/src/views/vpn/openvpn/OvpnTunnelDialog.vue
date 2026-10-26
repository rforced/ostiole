<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import ZoneField from '@/components/ZoneField.vue'
import { formatRemotes, parseRemotes } from '@/lib/ovpnconf'
import type { Interface, OpenVPN } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import OvpnFileDialog from '@/views/vpn/openvpn/OvpnFileDialog.vue'

const props = defineProps({ tunnel: { type: Object as PropType<Interface | null>, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const auth = useAuthStore()
const config = useConfigStore()
const form = ref(blank())
const error = ref('')
const showPassword = ref(false)
const clearPassword = ref(false)
const replacing = ref(false)
/** Certificates and keys from Replace from file, until Save. */
const blocks = ref<Partial<OpenVPN> | null>(null)
const zoneField = ref<InstanceType<typeof ZoneField> | null>(null)

const BLOCK_KEYS = [
  'caPem',
  'certPem',
  'keyPem',
  'tlsCryptKeyPem',
  'tlsAuthKeyPem',
  'keyDirection',
] as const

interface TunnelForm {
  description: string
  remotes: string
  random: boolean
  username: string
  password: string
  verify: string
  routes: string
  pushed: boolean
  keepalive: number | string
  mtu: number | string
  zone: string
  enabled: boolean
}

function blank(): TunnelForm {
  return {
    description: '',
    remotes: '',
    random: false,
    username: '',
    password: '',
    verify: '',
    routes: '',
    pushed: false,
    keepalive: '',
    mtu: '',
    zone: '',
    enabled: true,
  }
}

const routesText = (o?: OpenVPN) => (o?.routes ?? []).join(', ')

watch(
  () => [open.value, props.tunnel],
  () => {
    if (!open.value || !props.tunnel) return
    error.value = ''
    showPassword.value = false
    clearPassword.value = false
    blocks.value = null
    const t = props.tunnel
    const o = t.openvpn
    if (!o) return
    form.value = {
      description: t.description ?? '',
      remotes: formatRemotes(o.remotes),
      random: Boolean(o.remoteRandom),
      username: o.username ?? '',
      password: '',
      verify: o.verifyName ?? '',
      routes: routesText(o),
      pushed: Boolean(o.pushedRoutes),
      keepalive: o.keepalive || '',
      mtu: t.mtu || '',
      zone: t.zone ?? '',
      enabled: t.enabled,
    }
  },
  { immediate: true },
)

/** The blocks as they will be saved: replaced ones, or the tunnel's own. */
const held = computed((): Partial<OpenVPN> => blocks.value ?? props.tunnel?.openvpn ?? {})
const savedPassword = computed(() => Boolean(props.tunnel?.openvpn?.password))

function save() {
  error.value = ''
  const f = form.value
  const t = props.tunnel
  if (!t?.openvpn) return
  // What this dialog does not show stays as it was.
  const iface: Interface = JSON.parse(JSON.stringify(t))
  const o = iface.openvpn
  if (!o) return
  if (f.remotes !== formatRemotes(t.openvpn.remotes)) {
    const got = parseRemotes(f.remotes)
    if (got.error) {
      error.value = got.error
      return
    }
    o.remotes = got.remotes
  }
  if (f.routes !== routesText(t.openvpn)) {
    const routes = f.routes
      .split(',')
      .map((r) => r.trim())
      .filter(Boolean)
    if (routes.length) o.routes = routes
    else delete o.routes
  }
  if (!zoneField.value?.commit()) return
  if (f.random) o.remoteRandom = true
  else delete o.remoteRandom
  if (f.pushed) o.pushedRoutes = true
  else delete o.pushedRoutes
  if (f.verify.trim()) o.verifyName = f.verify.trim()
  else delete o.verifyName
  if (Number(f.keepalive)) o.keepalive = Number(f.keepalive)
  else delete o.keepalive
  if (f.username.trim()) {
    o.username = f.username.trim()
    if (f.password) o.password = f.password
    else if (clearPassword.value) delete o.password
  } else {
    delete o.username
    delete o.password
  }
  if (blocks.value) {
    for (const k of BLOCK_KEYS) Reflect.deleteProperty(o, k)
    Object.assign(o, blocks.value)
  }
  if (f.description.trim()) iface.description = f.description.trim()
  else delete iface.description
  if (Number(f.mtu)) iface.mtu = Number(f.mtu)
  else delete iface.mtu
  if (f.zone) iface.zone = f.zone
  else delete iface.zone
  iface.enabled = f.enabled
  config.upsertInterface(iface)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="tunnel ? `Tunnel ${tunnel.name}` : 'Tunnel'"
    description="Its zone decides which rules apply to what comes out of it."
  >
    <form v-if="tunnel" id="ovpn-tunnel-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="ovt-desc" label="Description">
          <input id="ovt-desc" v-model="form.description" class="input" />
        </FormField>
        <ZoneField
          id="ovt-zone"
          ref="zoneField"
          v-model="form.zone"
          :iface="tunnel.name"
          :suggested="tunnel.name"
        />
      </div>

      <FormField id="ovt-remotes" label="Remotes" hint="One per line: host, port and udp or tcp.">
        <textarea
          id="ovt-remotes"
          v-model="form.remotes"
          rows="3"
          class="input font-mono text-code"
          spellcheck="false"
          autocomplete="off"
          required
        ></textarea>
      </FormField>
      <ToggleRow
        id="ovt-random"
        v-model="form.random"
        label="Pick a remote at random"
        hint="Off tries them in the order listed."
      />

      <div class="fields">
        <FormField id="ovt-username" label="Username" hint="Empty signs in by certificate only.">
          <input
            id="ovt-username"
            v-model="form.username"
            class="input"
            autocomplete="off"
            spellcheck="false"
            placeholder="No sign-in"
          />
        </FormField>
        <FormField
          id="ovt-password"
          label="Password"
          :hint="savedPassword ? 'Set. Empty keeps the saved one.' : 'Kept on this router.'"
        >
          <div class="flex gap-2">
            <input
              id="ovt-password"
              v-model="form.password"
              :type="showPassword ? 'text' : 'password'"
              class="input"
              autocomplete="new-password"
              :placeholder="savedPassword && !clearPassword ? 'Saved password' : 'No password'"
            />
            <button type="button" class="btn-secondary" @click="showPassword = !showPassword">
              {{ showPassword ? 'Hide' : 'Show' }}
            </button>
            <button
              v-if="savedPassword && !clearPassword && !auth.readOnly"
              type="button"
              class="btn-secondary"
              @click="clearPassword = true"
            >
              Clear
            </button>
          </div>
        </FormField>
      </div>

      <FormField
        id="ovt-verify"
        label="Server name"
        hint="The name the server's certificate must carry. Empty takes any the CA signed."
      >
        <input
          id="ovt-verify"
          v-model="form.verify"
          class="input font-mono"
          spellcheck="false"
          placeholder="Any the CA signed"
        />
      </FormField>
      <FormField
        id="ovt-routes"
        label="Routes"
        hint="Networks reached through the tunnel, comma separated."
      >
        <input
          id="ovt-routes"
          v-model="form.routes"
          class="input font-mono"
          spellcheck="false"
          placeholder="None"
        />
      </FormField>
      <ToggleRow
        id="ovt-pushed"
        v-model="form.pushed"
        label="Use the routes the server sends"
        hint="The router's default route and DNS are never taken."
      />
      <div class="fields">
        <FormField id="ovt-keepalive" label="Keepalive (seconds)" hint="10 is the default.">
          <input
            id="ovt-keepalive"
            v-model="form.keepalive"
            type="number"
            min="0"
            max="3600"
            class="input w-32 font-mono max-sm:w-full"
            placeholder="10"
          />
        </FormField>
        <FormField id="ovt-mtu" label="MTU" hint="Empty takes the server's.">
          <input
            id="ovt-mtu"
            v-model="form.mtu"
            type="number"
            min="0"
            class="input w-32 font-mono max-sm:w-full"
            placeholder="The server's"
          />
        </FormField>
      </div>

      <dl class="kv">
        <dt>CA certificate</dt>
        <dd>{{ held.caPem ? 'set' : 'not set' }}</dd>
        <dt>Certificate and key</dt>
        <dd>{{ held.certPem && held.keyPem ? 'set' : 'not set' }}</dd>
        <template v-if="held.tlsCryptKeyPem">
          <dt>tls-crypt key</dt>
          <dd>set</dd>
        </template>
        <template v-if="held.tlsAuthKeyPem">
          <dt>tls-auth key</dt>
          <dd>set, direction {{ held.keyDirection ?? 1 }}</dd>
        </template>
      </dl>
      <p v-if="blocks" class="text-sm text-ink-muted">Replaced; Save to draft keeps them.</p>
      <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="replacing = true">
        Replace from file
      </button>

      <ToggleRow id="ovt-enabled" v-model="form.enabled" label="Enabled" />
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="ovpn-tunnel-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
  <OvpnFileDialog v-model:open="replacing" blocks-only @blocks="blocks = $event" />
</template>
