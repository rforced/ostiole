<script setup>
import { Plus } from 'lucide-vue-next'
import { TabsContent } from 'reka-ui'
import { computed, onMounted, ref, watch } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import PageHeader from '@/components/PageHeader.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { someOf } from '@/lib/lists'
import { usePageTabs } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import AggregateDialog from '@/views/interfaces/AggregateDialog.vue'
import InterfaceDialog from '@/views/interfaces/InterfaceDialog.vue'
import PppoeDialog from '@/views/interfaces/PppoeDialog.vue'
import VlanDialog from '@/views/interfaces/VlanDialog.vue'
import ZoneDialog from '@/views/interfaces/ZoneDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const { tabs, tab } = usePageTabs()
const links = ref([])

const editing = ref(null)
const editOpen = ref(false)
const vlanOpen = ref(false)
const aggOpen = ref(false)
const aggKind = ref('bridge')
const aggEditing = ref(null)
const pppOpen = ref(false)
const pppEditing = ref(null)
const pppoeReady = ref(true)
const zoneEditing = ref(null)
const zoneOpen = ref(false)

const live = useAsync(async () => {
  links.value = await api.interfaces.live()
})
const refreshLive = () => live.run()

/** The interface whose lease is being renewed, while it is. */
const renewing = ref('')
const renewError = ref('')

/** True when the interface holds a lease or a learned prefix rather than a configured address. */
function dynamic(c) {
  return !!c && (c.ipv4?.mode === 'dhcp' || ['dhcp', 'slaac', 'delegated'].includes(c.ipv6?.mode))
}

/**
 * Renew the lease on a row's interface. The server answers as soon as
 * networkd has been asked, before the lease has changed, so the links
 * are read again after a moment rather than straight away.
 */
async function renew(row, release) {
  const name = row.cfg.name
  renewing.value = name
  renewError.value = ''
  try {
    await api.interfaces.renew(name, release)
    await new Promise((resolve) => setTimeout(resolve, 1500))
    await refreshLive()
  } catch (e) {
    renewError.value = `${name}: ${e.message}`
  } finally {
    renewing.value = ''
  }
}

/**
 * True once both the draft and the live links have been read. Until then
 * the table shows a placeholder rather than whichever half arrived first:
 * rows sort by kernel index, so a config-only first paint would reorder
 * itself a moment later.
 */
const ready = ref(false)

// An apply creates and destroys real devices, so the live column has to
// be read again: a VLAN removed from the draft is gone from the kernel
// once the apply lands, and should leave this table with it.
watch(() => config.applied, refreshLive)

onMounted(async () => {
  await Promise.all([config.load(), refreshLive()])
  ready.value = true
  try {
    pppoeReady.value = (await api.services.status()).pppoeSetUp
  } catch {
    pppoeReady.value = true
  }
})

/** Live links (minus loopback) merged with draft config by name. */
const rows = computed(() => {
  if (!ready.value) return []
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

function addPppoe() {
  pppEditing.value = null
  pppOpen.value = true
}

function editPppoe(cfg) {
  pppEditing.value = cfg
  pppOpen.value = true
}

function editAggregate(cfg) {
  aggKind.value = cfg.bond ? 'bond' : 'bridge'
  aggEditing.value = cfg
  aggOpen.value = true
}

/** The per-interface guards worth seeing at a glance. */
function guards(c) {
  if (!c) return []
  const out = []
  if (c.blockPrivate) out.push('blocks private')
  if (c.blockBogons) out.push('blocks bogons')
  if (c.logDrops === true) out.push('logs drops')
  if (c.logDrops === false) out.push('drops quietly')
  return out
}

/**
 * The page an interface belongs to, or null. A tunnel, a tailnet node and
 * a wireless network are made there and deleted there: the interface is
 * what the configuration produces, not the thing itself, and only that
 * page knows what goes with it — a tunnel's peers and its private key, a
 * network's passphrase.
 */
function ownerPage(cfg) {
  if (cfg?.wireguard) return { to: '/vpn/wireguard', label: 'WireGuard' }
  if (cfg?.tailscale) return { to: '/vpn/tailscale', label: 'Tailscale' }
  if (cfg?.wireless) return { to: '/wireless', label: 'Wireless' }
  return null
}

/** A one-line description of what an interface is made of. */
function describeKind(row) {
  const c = row.cfg
  if (c?.pppoe) return `PPPoE over ${c.pppoe.parent} as ${c.pppoe.username}`
  if (c?.bridge) return `bridge of ${c.bridge.members.join(', ') || 'nothing yet'}`
  if (c?.bond) return `${c.bond.mode} bond of ${c.bond.members.join(', ') || 'nothing yet'}`
  if (c?.vlan) return `VLAN ${c.vlan.id} on ${c.vlan.parent}`
  if (c?.wireless) return `network "${c.wireless.ssid}" on ${c.wireless.radio}`
  if (c?.tailscale) return 'tailscale'
  if (c?.wireguard) return 'wireguard'
  const l = row.live
  if (!l) return 'not present on this system'
  if (l.wireless) return 'wireless'
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
  <div class="space-y-5">
    <PageHeader title="Interfaces" />
    <p v-if="config.error || live.error.value || renewError" role="alert" class="text-sm text-bad">
      {{ config.error || live.error.value || renewError }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-ink-muted">
      No configuration yet.
      <template v-if="!auth.readOnly">
        <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
      </template>
    </p>

    <AppTabs v-else-if="config.draft" v-model="tab" :tabs="tabs">
      <TabsContent value="interfaces">
        <SectionCard title="Interfaces" :count="rows.length" flush>
          <template #actions>
            <RefreshButton
              :busy="live.busy.value"
              :updated-at="live.updatedAt.value"
              @click="refreshLive"
            />
            <template v-if="!auth.readOnly">
              <button type="button" class="btn-secondary" @click="vlanOpen = true">
                <Plus class="size-4" aria-hidden="true" /> Add VLAN
              </button>
              <button type="button" class="btn-secondary" @click="addAggregate('bridge')">
                <Plus class="size-4" aria-hidden="true" /> Add bridge
              </button>
              <button type="button" class="btn-secondary" @click="addAggregate('bond')">
                <Plus class="size-4" aria-hidden="true" /> Add bond
              </button>
              <button type="button" class="btn-secondary" @click="addPppoe">
                <Plus class="size-4" aria-hidden="true" /> Add PPPoE
              </button>
            </template>
          </template>
          <table class="table table-stack">
            <thead>
              <tr>
                <th>Interface</th>
                <th>Link</th>
                <th>Addresses</th>
                <th>Zone</th>
                <th>Configured addressing</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!rows.length">
                <td colspan="6" class="text-ink-muted">
                  {{ ready ? 'No links on this system.' : 'Reading…' }}
                </td>
              </tr>
              <tr
                v-for="row in rows"
                :key="row.live?.name ?? row.cfg.name"
                :class="{ 'row-changed': config.isChanged('interfaces', row.cfg?.name) }"
              >
                <td data-label="">
                  <div class="font-mono font-medium">{{ row.live?.name ?? row.cfg.name }}</div>
                  <div class="text-xs text-ink-muted">
                    {{ describeKind(row) }}<span v-if="row.live?.mac"> · {{ row.live.mac }}</span
                    ><span v-else-if="row.cfg && !row.live"> · not present yet</span>
                  </div>
                </td>
                <td data-label="Link">
                  <span v-if="!row.live" class="badge" :class="{ 'badge-warn': row.cfg }">
                    absent
                  </span>
                  <span v-else-if="row.live.carrier" class="badge badge-ok">up</span>
                  <span v-else-if="row.live.up" class="badge badge-warn">no carrier</span>
                  <!-- Down is a fault on a link the router is meant to use. -->
                  <span v-else class="badge" :class="{ 'badge-warn': row.cfg?.enabled }">down</span>
                </td>
                <td class="font-mono text-code" data-label="Addresses">
                  {{ row.live?.addresses.join(' ') || '—' }}
                </td>
                <td data-label="Zone">
                  <span v-if="row.cfg?.zone" class="font-mono">{{ row.cfg.zone }}</span>
                  <span v-else-if="row.cfg" class="text-ink-muted">unassigned</span>
                  <span v-else class="text-ink-muted">not managed</span>
                </td>
                <td class="font-mono text-code" data-label="Addressing">
                  {{ describeAddressing(row.cfg)
                  }}<span v-if="row.cfg && !row.cfg.enabled" class="ml-1 text-ink-muted"
                    >(disabled)</span
                  >
                  <div v-if="guards(row.cfg).length" class="mt-1 space-x-1">
                    <span v-for="g in guards(row.cfg)" :key="g" class="badge">{{ g }}</span>
                  </div>
                </td>
                <td class="text-right whitespace-nowrap" data-label="">
                  <template v-if="row.live && dynamic(row.cfg) && !auth.readOnly">
                    <button
                      type="button"
                      class="link mr-3"
                      :disabled="renewing === row.cfg.name"
                      @click="renew(row, false)"
                    >
                      {{ renewing === row.cfg.name ? 'Renewing…' : 'Renew' }}
                    </button>
                    <ConfirmButton
                      class="mr-3"
                      label="Release"
                      :question="`Release the lease on ${row.cfg.name} and ask for a new one?`"
                      description="The address is gone until the server answers. A WAN is offline until then."
                      :danger="false"
                      @confirm="renew(row, true)"
                    />
                  </template>
                  <button
                    v-if="row.cfg?.bridge || row.cfg?.bond"
                    type="button"
                    class="link mr-3"
                    @click="editAggregate(row.cfg)"
                  >
                    Members
                  </button>
                  <RouterLink
                    v-if="ownerPage(row.cfg)"
                    :to="ownerPage(row.cfg).to"
                    class="link mr-3"
                  >
                    {{ ownerPage(row.cfg).label }}
                  </RouterLink>
                  <button
                    v-if="row.cfg?.pppoe"
                    type="button"
                    class="link"
                    @click="editPppoe(row.cfg)"
                  >
                    {{ auth.readOnly ? 'View' : 'Edit' }}
                  </button>
                  <button v-else-if="row.cfg" type="button" class="link" @click="edit(row)">
                    {{ auth.readOnly ? 'View' : 'Edit' }}
                  </button>
                  <button v-else-if="!auth.readOnly" type="button" class="link" @click="edit(row)">
                    Edit
                  </button>
                  <ConfirmButton
                    v-if="row.cfg && !ownerPage(row.cfg)"
                    class="ml-3"
                    label="Delete"
                    :question="`Delete ${row.cfg.name} from the configuration?`"
                    description="Its addresses, zone, and settings go on the next apply. The device itself stays."
                    :dependents="config.interfaceDependents(row.cfg.name)"
                    dependents-label="Also deleted"
                    :typed="row.cfg.name"
                    @confirm="config.removeInterface(row.cfg.name)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </SectionCard>
      </TabsContent>

      <TabsContent value="zones">
        <SectionCard
          title="Zones"
          :count="config.zones.length"
          intro="A zone groups the interfaces that share a set of rules."
          flush
        >
          <template v-if="!auth.readOnly" #actions>
            <button type="button" class="btn-secondary" @click="editZone(null)">
              <Plus class="size-4" aria-hidden="true" /> Add zone
            </button>
          </template>
          <table class="table table-stack">
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
              <tr v-if="!config.zones.length">
                <td colspan="5" class="text-ink-muted">
                  No zones. Every interface needs one before it gets rules.
                </td>
              </tr>
              <tr
                v-for="z in config.zones"
                :key="z.name"
                :class="{ 'row-changed': config.isChanged('zones', z.name) }"
              >
                <td class="font-mono font-medium" data-label="">{{ z.name }}</td>
                <td data-label="Description">{{ z.description }}</td>
                <td class="font-mono text-code" data-label="Interfaces">
                  {{ config.zoneInterfaces(z.name).join(' ') || '—' }}
                </td>
                <td class="space-x-1" data-label="Flags">
                  <span v-if="z.external" class="badge">external</span>
                  <span v-if="z.antiLockout" class="badge badge-ok">anti-lockout</span>
                  <span v-if="z.logDrops" class="badge">log drops</span>
                </td>
                <td class="text-right whitespace-nowrap" data-label="">
                  <button type="button" class="link" @click="editZone(z)">
                    {{ auth.readOnly ? 'View' : 'Edit' }}
                  </button>
                  <ConfirmButton
                    v-if="!config.zoneInterfaces(z.name).length"
                    class="ml-3"
                    label="Delete"
                    :question="`Delete zone ${z.name}?`"
                    :dependents="config.zoneDependents(z.name)"
                    :typed="z.name"
                    :disabled="z.antiLockout && !auth.isAdmin"
                    @confirm="config.removeZone(z.name)"
                  />
                  <span
                    v-else
                    class="ml-3 text-sm text-ink-muted"
                    :title="config.zoneInterfaces(z.name).join(', ')"
                  >
                    In use by {{ someOf(config.zoneInterfaces(z.name)) }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </SectionCard>
      </TabsContent>
    </AppTabs>

    <InterfaceDialog v-model:open="editOpen" :iface="editing" :links="links" />
    <VlanDialog v-model:open="vlanOpen" :parents="vlanParents" />
    <PppoeDialog
      v-model:open="pppOpen"
      :candidates="aggCandidates"
      :iface="pppEditing"
      :ready="pppoeReady"
    />
    <AggregateDialog
      v-model:open="aggOpen"
      :kind="aggKind"
      :candidates="aggCandidates"
      :iface="aggEditing"
    />
    <ZoneDialog v-model:open="zoneOpen" :zone="zoneEditing" />
  </div>
</template>
