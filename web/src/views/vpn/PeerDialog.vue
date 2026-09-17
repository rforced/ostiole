<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

const props = defineProps({
  tunnel: { type: Object, default: null },
  peer: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const confirm = useConfirmStore()
const form = ref(blank())
const error = ref('')

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
  }
}

watch(
  () => [open.value, props.peer],
  () => {
    if (!open.value) return
    error.value = ''
    const p = props.peer
    form.value = p
      ? { ...blank(), ...p, allowedIps: (p.allowedIps ?? []).join(', ') }
      : { ...blank(), allowedIps: suggestAddress() }
  },
  { immediate: true },
)

/** Next free host address inside the tunnel's own subnet. */
function suggestAddress() {
  const cidr = props.tunnel?.ipv4?.address
  if (!cidr) return ''
  const [addr] = cidr.split('/')
  const octets = addr.split('.')
  if (octets.length !== 4) return ''
  const used = new Set((props.tunnel.wireguard?.peers ?? []).flatMap((p) => p.allowedIps ?? []))
  for (let host = Number(octets[3]) + 1; host < 255; host += 1) {
    const candidate = `${octets[0]}.${octets[1]}.${octets[2]}.${host}/32`
    if (!used.has(candidate)) return candidate
  }
  return ''
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

function save() {
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

  config.upsertPeer(props.tunnel.name, out, props.peer?.name ?? out.name)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="peer ? `Peer ${peer.name}` : 'New peer'"
    :description="`On ${tunnel?.name ?? 'the tunnel'}. The peer's own device generates its key pair. Paste its public key here.`"
  >
    <form class="space-y-4" @submit.prevent="save">
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
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
      <FormField id="pe-pub" label="Public key">
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
        hint="What this peer may send from, and what gets routed to it. Comma separated."
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
          hint="host:port, only for peers this firewall dials."
        >
          <input
            id="pe-endpoint"
            v-model="form.endpoint"
            class="input font-mono"
            spellcheck="false"
          />
        </FormField>
        <FormField id="pe-keep" label="Keepalive" hint="Seconds; 25 keeps a NAT binding open.">
          <input
            id="pe-keep"
            v-model="form.keepalive"
            type="number"
            min="0"
            max="65535"
            class="input w-32 font-mono"
          />
        </FormField>
      </div>
      <FormField id="pe-psk" label="Preshared key" hint="Optional. The peer needs the same value.">
        <input id="pe-psk" v-model="form.presharedKey" class="input font-mono" />
      </FormField>
      <button type="button" class="btn-secondary" @click="generatePSK">
        Generate preshared key
      </button>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
