<script setup>
import { FileUp, Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { connected, livePeer } from '@/lib/wgStatus'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import TunnelDialog from '@/views/vpn/TunnelDialog.vue'
import TunnelFileDialog from '@/views/vpn/TunnelFileDialog.vue'

const props = defineProps({
  /** Each tunnel of the running configuration as its device has it, by name. */
  live: { type: Map, required: true },
  /** What the gateway monitor says of each gateway. */
  gateways: { type: Array, required: true },
  /** The page's read of both, from useAsync. */
  read: { type: Object, required: true },
})

const auth = useAuthStore()
const config = useConfigStore()
const editing = ref(null)
const open = ref(false)
const fromFile = ref(false)

const tunnels = computed(() => config.tunnels)

/** Peers go with their tunnel, so the dialog lists them first. */
function dependents(t) {
  return [
    ...(t.wireguard.peers ?? []).map((p) => `peer ${p.name}`),
    ...config.interfaceDependents(t.name),
  ]
}

/** How many of the tunnel's peers are sending now, out of those switched on. */
function peers(t) {
  const on = (t.wireguard.peers ?? []).filter((p) => p.enabled)
  if (!on.length) return 'none'
  if (!props.read.updatedAt.value) return '…'
  const now = Date.now()
  const n = on.filter((p) => connected(livePeer(props.live, t.name, p.publicKey), now)).length
  return `${n} of ${on.length} connected`
}

/** The gateways that send rule traffic into the tunnel, with the monitor's word on each. */
function gatewaysOf(t) {
  return config.gateways
    .filter((g) => g.enabled && g.interface === t.name && !g.address)
    .map((g) => ({ name: g.name, live: props.gateways.find((s) => s.name === g.name) ?? null }))
}

function add() {
  editing.value = null
  open.value = true
}
function edit(t) {
  editing.value = t
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Tunnels" :count="tunnels.length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="fromFile = true">
          <FileUp class="size-4" aria-hidden="true" /> Add from file
        </button>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add tunnel
        </button>
      </template>
      <div v-if="read.error.value" class="card-strip">
        <p role="alert" class="text-bad">The tunnels could not be read: {{ read.error.value }}</p>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Tunnel</th>
            <th>Zone</th>
            <th>Addresses</th>
            <th>Listening on</th>
            <th>Public key</th>
            <th>Peers</th>
            <th>Gateway</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!tunnels.length">
            <td colspan="8" class="text-ink-muted">No tunnels. Adding one makes its key pair.</td>
          </tr>
          <tr
            v-for="t in tunnels"
            :key="t.name"
            :class="{
              'opacity-50': !t.enabled,
              'row-changed': config.isChanged('interfaces', t.name),
            }"
          >
            <td data-label="">
              <div class="font-mono">{{ t.name }}</div>
              <div v-if="t.description" class="text-xs text-ink-muted">{{ t.description }}</div>
              <span v-if="!t.enabled" class="badge badge-warn">disabled</span>
              <span
                v-else-if="live.get(t.name)"
                class="badge"
                :class="live.get(t.name).up ? 'badge-ok' : 'badge-warn'"
                >{{ live.get(t.name).up ? 'up' : 'down' }}</span
              >
            </td>
            <td class="font-mono text-code" data-label="Zone">{{ t.zone || 'unassigned' }}</td>
            <td class="font-mono text-code" data-label="Addresses">
              <div v-for="a in [t.ipv4?.address, t.ipv6?.address].filter(Boolean)" :key="a">
                {{ a }}
              </div>
              <template v-if="!t.ipv4?.address && !t.ipv6?.address">—</template>
            </td>
            <td class="font-mono text-code" data-label="Listening on">
              {{ t.wireguard.listenPort ? `udp/${t.wireguard.listenPort}` : 'only dials out' }}
            </td>
            <td
              class="max-w-56 font-mono text-code break-all max-sm:max-w-none"
              data-label="Public key"
            >
              {{ t.wireguard.publicKey || 'unknown' }}
            </td>
            <td class="whitespace-nowrap" data-label="Peers">{{ peers(t) }}</td>
            <td data-label="Gateway">
              <div v-for="g in gatewaysOf(t)" :key="g.name" class="whitespace-nowrap">
                <RouterLink to="/routing" class="link font-mono">{{ g.name }}</RouterLink>
                <span
                  v-if="g.live && !g.live.unknown"
                  class="badge ml-1"
                  :class="g.live.online ? 'badge-ok' : 'badge-warn'"
                  >{{ g.live.online ? 'up' : 'down' }}</span
                >
              </div>
              <template v-if="!gatewaysOf(t).length">—</template>
            </td>
            <td class="actions" data-label="">
              <button type="button" class="link-action" @click="edit(t)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete tunnel ${t.name}?`"
                description="Peers lose their way in once this is applied. The private key is not kept."
                :dependents="dependents(t)"
                :typed="t.name"
                @confirm="config.removeTunnel(t.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <TunnelDialog v-model:open="open" :tunnel="editing" />
    <TunnelFileDialog v-model:open="fromFile" />
  </div>
</template>
