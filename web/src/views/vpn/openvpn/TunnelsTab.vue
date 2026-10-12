<script setup>
import { FileUp } from '@lucide/vue'
import { computed, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import SectionCard from '@/components/SectionCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatBytes } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import OvpnFileDialog from '@/views/vpn/openvpn/OvpnFileDialog.vue'
import OvpnTunnelDialog from '@/views/vpn/openvpn/OvpnTunnelDialog.vue'

const props = defineProps({
  /** Each tunnel as its daemon has it, by name. */
  live: { type: Map, required: true },
  /** Whether this router can run a tunnel at all. */
  setUp: { type: Boolean, default: true },
  /** Whether the kernel data channel module was skipped because Secure Boot is on. */
  secureBootSkipped: { type: Boolean, default: false },
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

const tunnels = computed(() => config.openvpnTunnels)

/** connected, connecting while the daemon runs, down when it does not. */
function state(t) {
  const l = props.live.get(t.name)
  if (!l) return null
  if (l.connected) return 'connected'
  return l.running ? 'connecting' : 'down'
}

function remote(t) {
  const [first, ...rest] = t.openvpn.remotes ?? []
  if (!first) return '—'
  return `${first.host}:${first.port}${rest.length ? ` +${rest.length}` : ''}`
}

function traffic(t) {
  const l = props.live.get(t.name)
  if (!l?.running) return '—'
  return `${formatBytes(l.bytesIn ?? 0)} in / ${formatBytes(l.bytesOut ?? 0)} out`
}

/** The gateways that send rule traffic into the tunnel, with the monitor's word on each. */
function gatewaysOf(t) {
  return config.gateways
    .filter((g) => g.enabled && g.interface === t.name && !g.address)
    .map((g) => ({ name: g.name, live: props.gateways.find((s) => s.name === g.name) ?? null }))
}

function edit(t) {
  editing.value = t
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <AppNotice v-if="!setUp">
      Not on this router. Run <code class="font-mono">ostiole repair --openvpn</code> as root once.
    </AppNotice>
    <AppNotice v-else-if="secureBootSkipped">
      The kernel data channel module was skipped because Secure Boot is on. Turn it off and run
      <code class="font-mono">ostiole repair --openvpn</code> as root; tunnels run in userspace
      until then.
    </AppNotice>
    <SectionCard title="Tunnels" :count="tunnels.length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-primary" @click="fromFile = true">
          <FileUp class="size-4" aria-hidden="true" /> Add from file
        </button>
      </template>
      <div v-if="read.error.value" class="card-strip">
        <ErrorLine>The tunnels could not be read: {{ read.error.value }}</ErrorLine>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Tunnel</th>
            <th>Zone</th>
            <th>Remote</th>
            <th>Data channel</th>
            <th>Traffic</th>
            <th>Gateway</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!tunnels.length">
            <td colspan="7" class="text-ink-muted">
              No tunnels. A provider's or a server's client file adds one.
            </td>
          </tr>
          <template v-for="t in tunnels" :key="t.name">
            <tr
              :class="{
                'opacity-50': !t.enabled,
                'row-changed': config.isChanged('interfaces', t.name),
              }"
            >
              <td data-label="">
                <div class="font-mono">{{ t.name }}</div>
                <div v-if="t.description" class="text-xs text-ink-muted">{{ t.description }}</div>
                <span v-if="!t.enabled" class="badge">disabled</span>
                <StatusBadge v-else-if="state(t)" :state="state(t)" />
              </td>
              <td class="font-mono text-code" data-label="Zone">{{ t.zone || 'unassigned' }}</td>
              <td class="font-mono text-code" data-label="Remote">{{ remote(t) }}</td>
              <td data-label="Data channel">{{ live.get(t.name)?.dataChannel ?? '' }}</td>
              <td class="whitespace-nowrap" data-label="Traffic">{{ traffic(t) }}</td>
              <td data-label="Gateway">
                <div
                  v-for="g in gatewaysOf(t)"
                  :key="g.name"
                  class="flex items-center gap-1.5 whitespace-nowrap"
                >
                  <RouterLink to="/routing" class="link font-mono">{{ g.name }}</RouterLink>
                  <StatusBadge
                    v-if="g.live && !g.live.unknown"
                    :state="g.live.online ? 'up' : 'down'"
                  />
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
                  description="Its certificates and credentials are not kept."
                  :dependents="config.interfaceDependents(t.name)"
                  :typed="t.name"
                  @confirm="config.removeInterface(t.name)"
                />
              </td>
            </tr>
            <tr v-if="live.get(t.name)?.note">
              <td colspan="7" class="text-sm text-ink-muted">{{ live.get(t.name).note }}</td>
            </tr>
          </template>
        </tbody>
      </table>
    </SectionCard>

    <OvpnTunnelDialog v-model:open="open" :tunnel="editing" />
    <OvpnFileDialog v-model:open="fromFile" />
  </div>
</template>
