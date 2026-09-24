<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import PageHeader from '@/components/PageHeader.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useConfigStore } from '@/stores/config'
import PeerDialog from '@/views/vpn/PeerDialog.vue'
import TunnelDialog from '@/views/vpn/TunnelDialog.vue'

const config = useConfigStore()
const tunnelEditing = ref(null)
const tunnelOpen = ref(false)
const peerTunnel = ref(null)
const peerEditing = ref(null)
const peerOpen = ref(false)

const tunnels = computed(() => config.tunnels)

/** Peers go with their tunnel, so the dialog lists them first. */
function tunnelDependents(t) {
  return [
    ...(t.wireguard.peers ?? []).map((p) => `peer ${p.name}`),
    ...config.interfaceDependents(t.name),
  ]
}

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
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <button type="button" class="btn-secondary" @click="addTunnel">
        <Plus class="size-4" aria-hidden="true" /> Add tunnel
      </button>
    </PageHeader>

    <p v-if="!tunnels.length" class="text-sm text-ink-muted">
      No tunnels. Adding one generates its key pair.
    </p>

    <TransitionGroup name="row" class="space-y-5" tag="div">
      <SectionCard v-for="t in tunnels" :key="t.name" flush>
        <template #title>
          <span class="font-mono">{{ t.name }}</span>
          <span v-if="t.description" class="font-normal text-ink-muted">{{ t.description }}</span>
          <span v-if="!t.enabled" class="badge badge-warn">disabled</span>
          <span v-if="config.isChanged('interfaces', t.name)" class="badge badge-warn">
            unapplied
          </span>
        </template>
        <template #actions>
          <button type="button" class="btn-secondary" @click="addPeer(t)">
            <Plus class="size-4" aria-hidden="true" /> Add peer
          </button>
          <button type="button" class="link ml-2" @click="editTunnel(t)">Edit</button>
          <ConfirmButton
            label="Delete"
            :question="`Delete tunnel ${t.name}?`"
            description="Peers lose their way in once this is applied. The private key is not kept."
            :dependents="tunnelDependents(t)"
            :typed="t.name"
            @confirm="config.removeTunnel(t.name)"
          />
        </template>

        <div class="card-strip">
          <dl class="kv">
            <dt>Zone</dt>
            <dd class="font-mono">{{ t.zone || 'unassigned' }}</dd>
            <dt>Address</dt>
            <dd class="font-mono">{{ t.ipv4?.address || '—' }}</dd>
            <dt>Listening on</dt>
            <dd class="font-mono">
              {{
                t.wireguard.listenPort ? `udp/${t.wireguard.listenPort}` : 'nothing (client only)'
              }}
            </dd>
            <dt>Public key</dt>
            <dd class="font-mono text-code break-all">{{ t.wireguard.publicKey || 'unknown' }}</dd>
          </dl>
        </div>
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
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!(t.wireguard.peers ?? []).length" key="empty" class="row-static">
              <td colspan="5" class="text-ink-muted">No peers.</td>
            </tr>
            <tr
              v-for="p in t.wireguard.peers ?? []"
              :key="p.name"
              :class="{
                'opacity-50': !p.enabled,
                'row-changed': config.isChanged(`interfaces[${t.name}].wireguard.peers`, p.name),
              }"
            >
              <td>
                <div class="font-mono">{{ p.name }}</div>
                <div class="text-xs text-ink-muted">{{ p.description }}</div>
              </td>
              <td class="max-w-56 font-mono text-code break-all">
                {{ p.publicKey }}
                <span v-if="p.presharedKey" class="badge ml-1">PSK</span>
              </td>
              <td class="font-mono text-code">{{ (p.allowedIps ?? []).join(', ') }}</td>
              <td class="font-mono text-code">
                {{ p.endpoint || '—'
                }}<span v-if="p.keepalive" class="text-ink-muted"> · {{ p.keepalive }}s</span>
              </td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editPeer(t, p)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete peer ${p.name}?`"
                  @confirm="config.removePeer(t.name, p.name)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </SectionCard>
    </TransitionGroup>

    <TunnelDialog v-model:open="tunnelOpen" :tunnel="tunnelEditing" />
    <PeerDialog v-model:open="peerOpen" :tunnel="peerTunnel" :peer="peerEditing" />
  </div>
</template>
