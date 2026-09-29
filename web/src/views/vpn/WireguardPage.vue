<script setup>
import { Plus } from 'lucide-vue-next'
import { TabsContent } from 'reka-ui'
import { computed, ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import PageHeader from '@/components/PageHeader.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes } from '@/lib/format'
import { usePageTabs } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import PeerDialog from '@/views/vpn/PeerDialog.vue'
import PeerLogTab from '@/views/vpn/PeerLogTab.vue'
import TunnelDialog from '@/views/vpn/TunnelDialog.vue'

/** How often the tunnels are read while the page is shown. */
const POLL_MS = 5000

const auth = useAuthStore()
const config = useConfigStore()
const { tabs, tab } = usePageTabs()
const tunnelEditing = ref(null)
const tunnelOpen = ref(false)
const peerTunnel = ref(null)
const peerEditing = ref(null)
const peerOpen = ref(false)

const tunnels = computed(() => config.tunnels)

/** Each tunnel of the running configuration as its device has it, by name. */
const live = ref(new Map())
const read = useAsync(
  async () => {
    const got = await api.wireguard.status()
    live.value = new Map(got.map((t) => [t.name, t]))
  },
  { interval: POLL_MS, immediate: true },
)

/** A peer as its tunnel's device sees it, found by its key. */
function livePeer(t, p) {
  return live.value.get(t.name)?.peers.find((q) => q.publicKey === p.publicKey)
}

/** When the peer last shook hands: "never" for one that has not. */
function handshake(t, p) {
  if (!read.updatedAt.value) return '…'
  const q = livePeer(t, p)
  if (!q) return '—'
  return q.lastHandshake ? new Date(q.lastHandshake).toLocaleString() : 'never'
}

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
      <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="addTunnel">
        <Plus class="size-4" aria-hidden="true" /> Add tunnel
      </button>
    </PageHeader>

    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="tunnels" class="space-y-5">
        <p v-if="!tunnels.length" class="text-sm text-ink-muted">
          No tunnels. Adding one generates its key pair.
        </p>
        <p v-if="read.error.value" role="alert" class="text-sm text-bad">
          The tunnels could not be read: {{ read.error.value }}
        </p>

        <div class="space-y-5">
          <SectionCard v-for="t in tunnels" :key="t.name" flush>
            <template #title>
              <span class="font-mono">{{ t.name }}</span>
              <span v-if="t.description" class="font-normal text-ink-muted">{{
                t.description
              }}</span>
              <span v-if="!t.enabled" class="badge badge-warn">disabled</span>
              <span
                v-else-if="live.get(t.name)"
                class="badge"
                :class="live.get(t.name).up ? 'badge-ok' : 'badge-warn'"
                >{{ live.get(t.name).up ? 'up' : 'down' }}</span
              >
              <span v-if="config.isChanged('interfaces', t.name)" class="badge badge-warn">
                unapplied
              </span>
            </template>
            <template #actions>
              <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="addPeer(t)">
                <Plus class="size-4" aria-hidden="true" /> Add peer
              </button>
              <button type="button" class="link ml-2" @click="editTunnel(t)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
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
                <dt>IPv4 address</dt>
                <dd class="font-mono">{{ t.ipv4?.address || '—' }}</dd>
                <dt>IPv6 address</dt>
                <dd class="font-mono">{{ t.ipv6?.address || '—' }}</dd>
                <dt>Listening on</dt>
                <dd class="font-mono">
                  {{
                    t.wireguard.listenPort
                      ? `udp/${t.wireguard.listenPort}`
                      : 'nothing (client only)'
                  }}
                </dd>
                <dt>Public key</dt>
                <dd class="font-mono text-code break-all">
                  {{ t.wireguard.publicKey || 'unknown' }}
                </dd>
              </dl>
            </div>
            <table class="table table-stack">
              <thead>
                <tr>
                  <th>Peer</th>
                  <th>Public key</th>
                  <th>Allowed addresses</th>
                  <th>Endpoint</th>
                  <th>Last handshake</th>
                  <th>Traffic</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="!(t.wireguard.peers ?? []).length">
                  <td colspan="7" class="text-ink-muted">No peers.</td>
                </tr>
                <tr
                  v-for="p in t.wireguard.peers ?? []"
                  :key="p.name"
                  :class="{
                    'opacity-50': !p.enabled,
                    'row-changed': config.isChanged(
                      `interfaces[${t.name}].wireguard.peers`,
                      p.name,
                    ),
                  }"
                >
                  <td data-label="">
                    <div class="font-mono">{{ p.name }}</div>
                    <div class="text-xs text-ink-muted">{{ p.description }}</div>
                  </td>
                  <td
                    class="max-w-56 font-mono text-code break-all max-sm:max-w-none"
                    data-label="Public key"
                  >
                    {{ p.publicKey }}
                    <span v-if="p.presharedKey" class="badge ml-1">PSK</span>
                  </td>
                  <td class="font-mono text-code" data-label="Allowed">
                    {{ (p.allowedIps ?? []).join(', ') }}
                  </td>
                  <td class="font-mono text-code" data-label="Endpoint">
                    {{ livePeer(t, p)?.endpoint || p.endpoint || '—'
                    }}<span v-if="p.keepalive" class="text-ink-muted"> · {{ p.keepalive }}s</span>
                    <div
                      v-if="
                        p.endpoint &&
                        livePeer(t, p)?.endpoint &&
                        livePeer(t, p).endpoint !== p.endpoint
                      "
                      class="font-sans text-xs text-ink-muted"
                    >
                      set to {{ p.endpoint }}
                    </div>
                  </td>
                  <td class="text-xs whitespace-nowrap text-ink-muted" data-label="Last handshake">
                    {{ handshake(t, p) }}
                  </td>
                  <td class="text-code" data-label="Traffic">
                    <template v-if="livePeer(t, p)">
                      <div>↓ {{ formatBytes(livePeer(t, p).rxBytes) }}</div>
                      <div>↑ {{ formatBytes(livePeer(t, p).txBytes) }}</div>
                    </template>
                    <template v-else>{{ read.updatedAt.value ? '—' : '…' }}</template>
                  </td>
                  <td class="text-right whitespace-nowrap" data-label="">
                    <button type="button" class="link" @click="editPeer(t, p)">
                      {{ auth.readOnly ? 'View' : 'Edit' }}
                    </button>
                    <ConfirmButton
                      class="ml-3"
                      label="Delete"
                      :question="`Delete peer ${p.name}?`"
                      :dependents="config.peerDependents(t.name, p.name)"
                      @confirm="config.removePeer(t.name, p.name)"
                    />
                  </td>
                </tr>
              </tbody>
            </table>
          </SectionCard>
        </div>
      </TabsContent>
      <TabsContent value="log"><PeerLogTab kind="wireguard" /></TabsContent>
    </AppTabs>

    <TunnelDialog v-model:open="tunnelOpen" :tunnel="tunnelEditing" />
    <PeerDialog v-model:open="peerOpen" :tunnel="peerTunnel" :peer="peerEditing" />
  </div>
</template>
