<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useConfigStore } from '@/stores/config'
import PeerDialog from '@/views/vpn/PeerDialog.vue'
import TunnelDialog from '@/views/vpn/TunnelDialog.vue'

const config = useConfigStore()
const tunnelEditing = ref(null)
const tunnelOpen = ref(false)
const peerTunnel = ref(null)
const peerEditing = ref(null)
const peerOpen = ref(false)

onMounted(() => config.load())

const tunnels = computed(() => config.tunnels)

function addTunnel() {
  tunnelEditing.value = null
  tunnelOpen.value = true
}
function editTunnel(t) {
  tunnelEditing.value = t
  tunnelOpen.value = true
}
function addPeer(tunnel) {
  peerTunnel.value = tunnel
  peerEditing.value = null
  peerOpen.value = true
}
function editPeer(tunnel, peer) {
  peerTunnel.value = tunnel
  peerEditing.value = peer
  peerOpen.value = true
}
function removePeer(tunnel, name) {
  config.removePeer(tunnel.name, name)
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between gap-4">
      <h1 class="text-2xl font-semibold tracking-tight">VPN</h1>
      <button v-if="config.draft" type="button" class="btn-secondary" @click="addTunnel">
        <Plus class="mr-1 size-4" aria-hidden="true" /> Add tunnel
      </button>
    </div>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>

    <template v-else-if="config.draft">
      <p class="max-w-3xl text-sm text-neutral-500">
        A WireGuard tunnel is an interface like any other: put it in a zone and the firewall rules,
        NAT, and DNS of that zone apply to it. Peers reach the listening port through the external
        zones, which Ostiole opens for you.
      </p>

      <p v-if="!tunnels.length" class="text-sm text-neutral-500">
        No tunnels yet. "Add tunnel" generates a key pair and a private network for it.
      </p>

      <section
        v-for="t in tunnels"
        :key="t.name"
        class="card space-y-3"
        :aria-label="`Tunnel ${t.name}`"
      >
        <div class="flex flex-wrap items-baseline justify-between gap-3">
          <h2 class="font-medium">
            <span class="font-mono">{{ t.name }}</span>
            <span v-if="t.description" class="ml-2 text-neutral-500">{{ t.description }}</span>
            <span v-if="!t.enabled" class="badge badge-warn ml-2">disabled</span>
          </h2>
          <div class="whitespace-nowrap">
            <button type="button" class="link" @click="editTunnel(t)">Edit</button>
            <ConfirmButton
              class="ml-3"
              label="Delete"
              confirm-label="Delete tunnel?"
              @confirm="config.removeInterface(t.name)"
            />
          </div>
        </div>
        <dl class="kv text-sm">
          <dt>Zone</dt>
          <dd class="font-mono">{{ t.zone || 'unassigned' }}</dd>
          <dt>Address</dt>
          <dd class="font-mono">{{ t.ipv4?.address || '—' }}</dd>
          <dt>Listening on</dt>
          <dd class="font-mono">
            {{ t.wireguard.listenPort ? `udp/${t.wireguard.listenPort}` : 'nothing (client only)' }}
          </dd>
          <dt>Public key</dt>
          <dd class="font-mono text-xs break-all">{{ t.wireguard.publicKey || 'unknown' }}</dd>
        </dl>

        <div class="flex items-center gap-3">
          <h3 class="font-medium">Peers</h3>
          <button type="button" class="btn-secondary" @click="addPeer(t)">
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add peer
          </button>
        </div>
        <div class="-mx-4 -mb-4 overflow-x-auto">
          <table class="table">
            <thead>
              <tr>
                <th>Peer</th>
                <th>Public key</th>
                <th>Allowed addresses</th>
                <th>Endpoint</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!(t.wireguard.peers ?? []).length">
                <td colspan="5" class="text-neutral-500">No peers yet.</td>
              </tr>
              <tr
                v-for="p in t.wireguard.peers ?? []"
                :key="p.name"
                :class="{ 'opacity-50': !p.enabled }"
              >
                <td>
                  <div class="font-mono">{{ p.name }}</div>
                  <div class="text-xs text-neutral-500">{{ p.description }}</div>
                </td>
                <td class="max-w-56 font-mono text-xs break-all">
                  {{ p.publicKey }}
                  <span v-if="p.presharedKey" class="badge ml-1">PSK</span>
                </td>
                <td class="font-mono text-xs">{{ (p.allowedIps ?? []).join(', ') }}</td>
                <td class="font-mono text-xs">
                  {{ p.endpoint || '—'
                  }}<span v-if="p.keepalive" class="text-neutral-500"> · {{ p.keepalive }}s</span>
                </td>
                <td class="text-right whitespace-nowrap">
                  <button type="button" class="link" @click="editPeer(t, p)">Edit</button>
                  <ConfirmButton
                    class="ml-3"
                    label="Delete"
                    confirm-label="Delete peer?"
                    @confirm="removePeer(t, p.name)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <TunnelDialog v-model:open="tunnelOpen" :tunnel="tunnelEditing" />
    <PeerDialog v-model:open="peerOpen" :tunnel="peerTunnel" :peer="peerEditing" />
  </div>
</template>
