<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

const props = defineProps({ tunnel: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const confirm = useConfirmStore()
const form = ref(blank())
const error = ref('')

function blank() {
  return {
    name: 'wg0',
    description: '',
    enabled: true,
    zone: '',
    address: '10.66.0.1/24',
    listenPort: 51820,
    mtu: 1420,
    privateKey: '',
    publicKey: '',
  }
}

watch(
  () => [open.value, props.tunnel],
  async () => {
    if (!open.value) return
    error.value = ''
    const t = props.tunnel
    if (t) {
      form.value = {
        name: t.name,
        description: t.description ?? '',
        enabled: t.enabled,
        zone: t.zone ?? '',
        address: t.ipv4?.address ?? '',
        listenPort: t.wireguard.listenPort ?? 0,
        mtu: t.mtu ?? 0,
        privateKey: t.wireguard.privateKey,
        publicKey: t.wireguard.publicKey ?? '',
      }
      return
    }
    form.value = blank()
    form.value.name = nextName()
    form.value.zone = config.zones.find((z) => !z.external)?.name ?? config.zones[0]?.name ?? ''
    await generate()
  },
  { immediate: true },
)

/** wg0, wg1, … whichever is free. */
function nextName() {
  const taken = new Set(config.interfaces.map((i) => i.name))
  for (let i = 0; i < 100; i += 1) if (!taken.has(`wg${i}`)) return `wg${i}`
  return 'wg0'
}

/**
 * A new tunnel gets its pair without asking. Replacing the pair of one
 * that exists strands every peer until it is handed the new public key.
 */
async function generate() {
  const name = form.value.name
  if (
    props.tunnel &&
    !(await confirm.ask({
      question: `Replace the key pair of ${name}?`,
      description: 'Every peer has to be given the new public key.',
      confirmLabel: 'Replace',
      typed: name,
    }))
  )
    return
  try {
    const keys = await api.wireguard.keys()
    form.value.privateKey = keys.privateKey
    form.value.publicKey = keys.publicKey
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e)
  }
}

function save() {
  const f = form.value
  const iface = {
    name: f.name.trim(),
    enabled: f.enabled,
    zone: f.zone,
    ipv4: f.address ? { mode: 'static', address: f.address.trim() } : { mode: 'none' },
    ipv6: { mode: 'none' },
    wireguard: {
      privateKey: f.privateKey.trim(),
      publicKey: f.publicKey.trim(),
      listenPort: Number(f.listenPort) || 0,
      peers: props.tunnel?.wireguard?.peers ?? [],
    },
  }
  if (f.description) iface.description = f.description
  if (Number(f.mtu)) iface.mtu = Number(f.mtu)
  config.upsertInterface(iface)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="tunnel ? `Tunnel ${tunnel.name}` : 'Add WireGuard tunnel'"
    description="Its zone decides which rules apply to what comes out of it."
  >
    <form class="space-y-4" @submit.prevent="save">
      <p v-if="error" role="alert" class="text-sm text-bad">{{ error }}</p>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="wg-name" label="Interface name">
          <input
            id="wg-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
            :disabled="!!tunnel"
          />
        </FormField>
        <FormField id="wg-desc" label="Description">
          <input id="wg-desc" v-model="form.description" class="input" />
        </FormField>
        <FormField id="wg-zone" label="Zone">
          <select id="wg-zone" v-model="form.zone" class="input">
            <option value="">Unassigned</option>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
        <FormField
          id="wg-addr"
          label="Address inside the tunnel"
          hint="CIDR, e.g. 10.66.0.1/24. Peers take addresses from it."
        >
          <input id="wg-addr" v-model="form.address" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField id="wg-port" label="Listen port" hint="0 for a tunnel that only dials out.">
          <input
            id="wg-port"
            v-model="form.listenPort"
            type="number"
            min="0"
            max="65535"
            class="input w-32 font-mono"
          />
        </FormField>
        <FormField id="wg-mtu" label="MTU" hint="1420 fits inside a 1500-byte path.">
          <input
            id="wg-mtu"
            v-model="form.mtu"
            type="number"
            min="0"
            class="input w-32 font-mono"
          />
        </FormField>
      </div>

      <FormField id="wg-pub" label="Public key" hint="Hand this to your peers.">
        <input id="wg-pub" :value="form.publicKey" class="input font-mono" readonly />
      </FormField>
      <div class="flex items-center gap-3 text-sm">
        <button type="button" class="btn-secondary" @click="generate">Generate new key pair</button>
        <span class="text-ink-muted">The private key never leaves this firewall.</span>
      </div>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.privateKey">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
