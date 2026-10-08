<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { formatBytes, formatWhen } from '@/lib/format'
import { byText, byTime, useSort } from '@/lib/sort'
import { connected, livePeer } from '@/lib/wgStatus'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import PeerDialog from '@/views/vpn/PeerDialog.vue'

const props = defineProps({
  /** Each tunnel of the running configuration as its device has it, by name. */
  live: { type: Map, required: true },
  /** The page's read of it, from useAsync. */
  read: { type: Object, required: true },
})

const auth = useAuthStore()
const config = useConfigStore()
const tunnel = ref(null)
const editing = ref(null)
const open = ref(false)

/** Every tunnel's peers, in the order the configuration has them. */
const rows = computed(() =>
  config.tunnels.flatMap((t) => (t.wireguard.peers ?? []).map((p) => ({ tunnel: t, peer: p }))),
)

/**
 * Live, on from the start. The page reads on for the Tunnels tab, so off
 * holds what the devices said, and when, as it went off.
 */
const following = ref(true)
const held = ref(null)
watch(following, (on) => {
  held.value = on ? null : { status: props.live, at: Date.now() }
  if (on) props.read.run()
})

/** A row's peer as its device reports it. */
const seen = (r) => livePeer(held.value?.status ?? props.live, r.tunnel.name, r.peer.publicKey)

const COLUMNS = [
  ['peer', 'Peer'],
  ['tunnel', 'Tunnel'],
  ['handshake', 'Last handshake'],
]
const sort = useSort(rows, {
  peer: byText((r) => r.peer.name),
  tunnel: byText((r) => r.tunnel.name),
  handshake: byTime((r) => seen(r)?.lastHandshake),
})
const peers = sort.sorted

/** The peer's allowed addresses, a network it shows under another prefix with it. */
function allowed(peer) {
  const shown = new Map((peer.theirs ?? []).map((m) => [m.network, m.as]))
  return (peer.allowedIps ?? [])
    .map((a) => (shown.has(a) ? `${a} as ${shown.get(a)}` : a))
    .join(', ')
}

/** When the peer last shook hands: "never" for one that has not. */
function handshake(r) {
  if (!props.read.updatedAt.value) return '…'
  const q = seen(r)
  if (!q) return '—'
  return formatWhen(q.lastHandshake, 'never')
}

/** The tunnel a new peer goes on first: one that takes calls, as devices need. */
function firstTunnel() {
  const on = config.tunnels.filter((t) => t.enabled)
  return on.find((t) => t.wireguard.listenPort) ?? on[0] ?? config.tunnels[0] ?? null
}

function add() {
  tunnel.value = firstTunnel()
  editing.value = null
  open.value = true
}
function edit(r) {
  tunnel.value = r.tunnel
  editing.value = r.peer
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Peers" :count="rows.length" flush>
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <LiveButton v-model="following" :failing="Boolean(read.error.value)" />
        <button
          v-if="!auth.readOnly"
          type="button"
          class="btn-secondary"
          :disabled="!config.tunnels.length"
          @click="add"
        >
          <Plus class="size-4" aria-hidden="true" /> Add peer
        </button>
      </template>
      <div v-if="read.error.value" class="card-strip">
        <ErrorLine>The tunnels could not be read: {{ read.error.value }}</ErrorLine>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <SortHeader by="peer" :sort="sort">Peer</SortHeader>
            <SortHeader by="tunnel" :sort="sort">Tunnel</SortHeader>
            <th>Allowed addresses</th>
            <th>Endpoint</th>
            <SortHeader by="handshake" :sort="sort">Last handshake</SortHeader>
            <th class="num">Traffic</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="7" class="text-ink-muted">
              {{ config.tunnels.length ? 'No peers.' : 'No peers. Add a tunnel first.' }}
            </td>
          </tr>
          <tr
            v-for="r in peers"
            :key="`${r.tunnel.name}/${r.peer.name}`"
            :class="{
              'opacity-50': !r.peer.enabled,
              'row-changed': config.isChanged(
                `interfaces[${r.tunnel.name}].wireguard.peers`,
                r.peer.name,
              ),
            }"
          >
            <td data-label="">
              <div class="font-mono">{{ r.peer.name }}</div>
              <div v-if="r.peer.description" class="text-xs text-ink-muted">
                {{ r.peer.description }}
              </div>
              <span class="inline-flex items-center gap-1.5">
                <span v-if="!r.peer.enabled" class="badge">disabled</span>
                <template v-else-if="read.updatedAt.value">
                  <span v-if="connected(seen(r), held?.at)" class="badge badge-ok">connected</span>
                  <span v-else class="badge">quiet</span>
                </template>
                <span v-if="r.peer.presharedKey" class="badge">PSK</span>
              </span>
            </td>
            <td class="font-mono text-code" data-label="Tunnel">{{ r.tunnel.name }}</td>
            <td class="font-mono text-code" data-label="Allowed">
              {{ allowed(r.peer) }}
              <div
                v-for="m in r.peer.ours ?? []"
                :key="m.network"
                class="font-sans text-xs text-ink-muted"
              >
                this side's {{ m.network }} there as {{ m.as }}
              </div>
            </td>
            <td class="font-mono text-code" data-label="Endpoint">
              {{ seen(r)?.endpoint || r.peer.endpoint || '—'
              }}<span v-if="r.peer.keepalive" class="text-ink-muted">
                · {{ r.peer.keepalive }}s</span
              >
              <div
                v-if="r.peer.endpoint && seen(r)?.endpoint && seen(r).endpoint !== r.peer.endpoint"
                class="font-sans text-xs text-ink-muted"
              >
                set to {{ r.peer.endpoint }}
              </div>
            </td>
            <td class="when" data-label="Last handshake">
              {{ handshake(r) }}
            </td>
            <td class="num text-code" data-label="Traffic">
              <template v-if="seen(r)">
                <div>↓ {{ formatBytes(seen(r).rxBytes) }}</div>
                <div>↑ {{ formatBytes(seen(r).txBytes) }}</div>
              </template>
              <template v-else>{{ read.updatedAt.value ? '—' : '…' }}</template>
            </td>
            <td class="actions" data-label="">
              <button type="button" class="link-action" @click="edit(r)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete peer ${r.peer.name}?`"
                :dependents="config.peerDependents(r.tunnel.name, r.peer.name)"
                @confirm="config.removePeer(r.tunnel.name, r.peer.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <PeerDialog v-model:open="open" :tunnel="tunnel" :peer="editing" />
  </div>
</template>
