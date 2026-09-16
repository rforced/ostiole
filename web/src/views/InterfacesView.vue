<script setup>
import { Plus, RefreshCw } from 'lucide-vue-next'
import { TabsContent } from 'reka-ui'
import { computed, onMounted, ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import AggregateDialog from '@/views/interfaces/AggregateDialog.vue'
import InterfaceDialog from '@/views/interfaces/InterfaceDialog.vue'
import VlanDialog from '@/views/interfaces/VlanDialog.vue'
import ZoneDialog from '@/views/interfaces/ZoneDialog.vue'

const config = useConfigStore()
const tab = ref('interfaces')
const links = ref([])
const liveError = ref('')

const editing = ref(null)
const editOpen = ref(false)
const vlanOpen = ref(false)
const aggOpen = ref(false)
const aggKind = ref('bridge')
const aggEditing = ref(null)
const zoneEditing = ref(null)
const zoneOpen = ref(false)

async function refreshLive() {
  try {
    links.value = await api.interfaces.live()
    liveError.value = ''
  } catch (e) {
    liveError.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(async () => {
  await Promise.all([config.load(), refreshLive()])
})

/** Live links (minus loopback) merged with draft config by name. */
const rows = computed(() => {
  const byName = new Map()
  for (const l of links.value) if (l.kind !== 'loopback') byName.set(l.name, { live: l, cfg: null })
  for (const c of config.interfaces) {
    const row = byName.get(c.name)
    if (row) row.cfg = c
    else byName.set(c.name, { live: null, cfg: c })
  }
  return [...byName.values()].sort((a, b) => (a.live?.index ?? 1e9) - (b.live?.index ?? 1e9))
})

const vlanParents = computed(() =>
  links.value.filter((l) => ['ethernet', 'bridge', 'bond'].includes(l.kind)),
)

/** Every link that could be a bridge or bond member. */
const aggCandidates = computed(() => links.value.filter((l) => l.kind !== 'loopback'))

function addAggregate(kind) {
  aggKind.value = kind
  aggEditing.value = null
  aggOpen.value = true
}

function editAggregate(cfg) {
  aggKind.value = cfg.bond ? 'bond' : 'bridge'
  aggEditing.value = cfg
  aggOpen.value = true
}

/** A one-line description of what an interface is made of. */
function describeKind(row) {
  const c = row.cfg
  if (c?.bridge) return `bridge of ${c.bridge.members.join(', ') || 'nothing yet'}`
  if (c?.bond) return `${c.bond.mode} bond of ${c.bond.members.join(', ') || 'nothing yet'}`
  if (c?.vlan) return `VLAN ${c.vlan.id} on ${c.vlan.parent}`
  const l = row.live
  if (!l) return 'not present on this system'
  if (l.master) return `port on ${l.master}`
  if (l.vlanId) return `VLAN ${l.vlanId} on ${l.parent}`
  return l.kind
}

function describeAddressing(c) {
  if (!c) return '—'
  const parts = []
  if (c.ipv4?.mode && c.ipv4.mode !== 'none')
    parts.push(c.ipv4.mode === 'static' ? c.ipv4.address : 'DHCP')
  const v6 = c.ipv6 ?? {}
  switch (v6.mode) {
    case 'static':
      parts.push(v6.address)
      break
    case 'delegated':
      parts.push(`subnet ${v6.subnetId ?? 0} of ${v6.delegatedFrom}`)
      break
    case 'dhcp':
      parts.push(v6.prefixHint ? `DHCPv6 + ${v6.prefixHint}` : 'DHCPv6')
      break
    case 'slaac':
      parts.push('SLAAC')
      break
  }
  return parts.length ? parts.join(', ') : 'no address'
}

function edit(row) {
  editing.value = row.cfg ?? {
    name: row.live.name,
    enabled: true,
    ipv4: { mode: 'none' },
    ipv6: { mode: 'none' },
  }
  editOpen.value = true
}

function editZone(z) {
  zoneEditing.value = z
  zoneOpen.value = true
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between gap-4">
      <h1 class="text-2xl font-semibold tracking-tight">Interfaces</h1>
    </div>
    <p v-if="config.error || liveError" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error || liveError }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>

    <AppTabs
      v-else-if="config.draft"
      v-model="tab"
      :tabs="[
        { value: 'interfaces', label: 'Interfaces' },
        { value: 'zones', label: 'Zones' },
      ]"
    >
      <TabsContent value="interfaces" class="space-y-3">
        <div class="flex gap-2">
          <button type="button" class="btn-secondary" @click="vlanOpen = true">
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add VLAN
          </button>
          <button type="button" class="btn-secondary" @click="addAggregate('bridge')">
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add bridge
          </button>
          <button type="button" class="btn-secondary" @click="addAggregate('bond')">
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add bond
          </button>
          <button type="button" class="btn-secondary" @click="refreshLive">
            <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Refresh
          </button>
        </div>
        <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
          <table class="table">
            <thead>
              <tr>
                <th>Interface</th>
                <th>Link</th>
                <th>Live addresses</th>
                <th>Zone</th>
                <th>Configured addressing</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in rows" :key="row.live?.name ?? row.cfg.name">
                <td>
                  <div class="font-mono font-medium">{{ row.live?.name ?? row.cfg.name }}</div>
                  <div class="text-xs text-neutral-500">
                    {{ describeKind(row) }}<span v-if="row.live?.mac"> · {{ row.live.mac }}</span
                    ><span v-else-if="row.cfg && !row.live"> · not present yet</span>
                  </div>
                </td>
                <td>
                  <span v-if="!row.live" class="badge">absent</span>
                  <span v-else-if="row.live.carrier" class="badge badge-ok">up</span>
                  <span v-else-if="row.live.up" class="badge badge-warn">no carrier</span>
                  <span v-else class="badge">down</span>
                </td>
                <td class="font-mono text-xs">{{ row.live?.addresses.join(' ') || '—' }}</td>
                <td>
                  <span v-if="row.cfg?.zone" class="font-mono">{{ row.cfg.zone }}</span>
                  <span v-else-if="row.cfg" class="text-neutral-500">unassigned</span>
                  <span v-else class="text-neutral-500">not managed</span>
                </td>
                <td class="font-mono text-xs">
                  {{ describeAddressing(row.cfg)
                  }}<span v-if="row.cfg && !row.cfg.enabled" class="ml-1 text-neutral-500"
                    >(disabled)</span
                  >
                </td>
                <td class="text-right whitespace-nowrap">
                  <button
                    v-if="row.cfg?.bridge || row.cfg?.bond"
                    type="button"
                    class="link mr-3"
                    @click="editAggregate(row.cfg)"
                  >
                    Members
                  </button>
                  <button type="button" class="link" @click="edit(row)">
                    {{ row.cfg ? 'Edit' : 'Configure' }}
                  </button>
                  <ConfirmButton
                    v-if="row.cfg"
                    class="ml-3"
                    label="Remove"
                    confirm-label="Remove from config?"
                    @confirm="config.removeInterface(row.cfg.name)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </TabsContent>

      <TabsContent value="zones" class="space-y-3">
        <button type="button" class="btn-secondary" @click="editZone(null)">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add zone
        </button>
        <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
          <table class="table">
            <thead>
              <tr>
                <th>Zone</th>
                <th>Description</th>
                <th>Interfaces</th>
                <th>Flags</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="z in config.zones" :key="z.name">
                <td class="font-mono font-medium">{{ z.name }}</td>
                <td>{{ z.description }}</td>
                <td class="font-mono text-xs">
                  {{
                    config.interfaces
                      .filter((i) => i.zone === z.name)
                      .map((i) => i.name)
                      .join(' ') || '—'
                  }}
                </td>
                <td class="space-x-1">
                  <span v-if="z.external" class="badge">external</span>
                  <span v-if="z.antiLockout" class="badge badge-ok">anti-lockout</span>
                  <span v-if="z.logDrops" class="badge">log drops</span>
                </td>
                <td class="text-right whitespace-nowrap">
                  <button type="button" class="link" @click="editZone(z)">Edit</button>
                  <ConfirmButton
                    v-if="config.zoneReferences(z.name).length === 0"
                    class="ml-3"
                    label="Delete"
                    confirm-label="Delete zone?"
                    @confirm="config.removeZone(z.name)"
                  />
                  <span
                    v-else
                    class="ml-3 text-xs text-neutral-500"
                    :title="config.zoneReferences(z.name).join(', ')"
                    >in use</span
                  >
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </TabsContent>
    </AppTabs>

    <InterfaceDialog v-model:open="editOpen" :iface="editing" />
    <VlanDialog v-model:open="vlanOpen" :parents="vlanParents" />
    <AggregateDialog
      v-model:open="aggOpen"
      :kind="aggKind"
      :candidates="aggCandidates"
      :iface="aggEditing"
    />
    <ZoneDialog v-model:open="zoneOpen" :zone="zoneEditing" />
  </div>
</template>
