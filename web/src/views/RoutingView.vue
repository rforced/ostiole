<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import PageHeader from '@/components/PageHeader.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import GatewayDialog from '@/views/routing/GatewayDialog.vue'
import GatewayGroupDialog from '@/views/routing/GatewayGroupDialog.vue'
import StaticRouteDialog from '@/views/routing/StaticRouteDialog.vue'

const config = useConfigStore()
const open = ref(false)
const editing = ref(null)
const gwOpen = ref(false)
const gwEditing = ref(null)
const groupOpen = ref(false)
const groupEditing = ref(null)
const live = ref([])
const policy = ref([])
const detected = ref([])

/** Configured gateways merged with what the monitor sees. */
const gatewayRows = computed(() =>
  config.gateways.map((g) => ({ ...g, live: live.value.find((l) => l.name === g.name) ?? null })),
)

/** Groups merged with where the kernel currently sends their traffic. */
const groupRows = computed(() =>
  config.gatewayGroups.map((g) => ({
    ...g,
    policy: policy.value.find((p) => p.name === g.name) ?? null,
  })),
)

/** Rules that pick a gateway, which is what policy routing exists for. */
const policyRules = computed(() => config.rules.filter((r) => r.gateway))

/** Each read is on its own: a monitor that is down leaves only its column blank. */
const refresh = useAsync(
  async () => {
    try {
      live.value = await api.gateways()
    } catch {
      live.value = []
    }
    try {
      policy.value = await api.policy()
    } catch {
      policy.value = []
    }
    try {
      detected.value = await api.detectedGateways()
    } catch {
      detected.value = []
    }
  },
  { interval: 10_000 },
)

/**
 * The gateway watching a detected route, if any. A gateway claims a route
 * when it is on the same interface and either names that address or names
 * none at all, which means "whatever the network gives us". The draft is
 * what is checked, not the saved configuration the server answers from: a
 * gateway you have just added stops being offered straight away, rather
 * than after you apply.
 */
function coveredBy(d) {
  const g = config.gateways.find(
    (g) => g.interface === d.interface && (!g.address || g.address === d.address),
  )
  return g?.name ?? ''
}

/** Adds a detected route as a gateway, ready to apply. */
function adopt(d) {
  config.upsertGateway(d.suggested)
}

function addGateway() {
  gwEditing.value = null
  gwOpen.value = true
}
function editGateway(g) {
  gwEditing.value = g
  gwOpen.value = true
}
function addGroup() {
  groupEditing.value = null
  groupOpen.value = true
}
function editGroup(g) {
  groupEditing.value = g
  groupOpen.value = true
}

/** The kernel's default route that this gateway is claiming, if any. */
function detectedFor(g) {
  return (
    detected.value.find(
      (d) =>
        d.configured === g.name ||
        (d.interface === g.interface && (!g.address || g.address === d.address)),
    ) ?? null
  )
}

/** Summarises a group's members as the tiers they fail over through. */
function tierSummary(group) {
  const tiers = new Map()
  for (const m of group.members ?? []) {
    const tier = m.tier ?? 0
    tiers.set(tier, [...(tiers.get(tier) ?? []), m.gateway])
  }
  return [...tiers.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([, names]) => names.join(' + '))
    .join(' → ')
}

onMounted(async () => {
  await config.load()
  await refresh.run()
})

function add() {
  editing.value = null
  open.value = true
}
function edit(r) {
  editing.value = r
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <PageHeader title="Routing">
      <RefreshButton
        :busy="refresh.busy.value"
        :updated-at="refresh.updatedAt.value"
        @click="refresh.run"
      />
    </PageHeader>
    <p v-if="config.error" role="alert" class="text-sm text-bad">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-ink-muted">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>
    <template v-else-if="config.draft">
      <SectionCard
        title="Gateways"
        :count="gatewayRows.length"
        intro="Each one is probed, and the default route moves off a gateway that stops answering."
        flush
      >
        <template #actions>
          <button type="button" class="btn-secondary" @click="addGateway">
            <Plus class="size-4" aria-hidden="true" /> Add gateway
          </button>
        </template>
        <table class="table">
          <thead>
            <tr>
              <th>Gateway</th>
              <th>Interface</th>
              <th>Address</th>
              <th>Monitor</th>
              <th>Priority</th>
              <th>State</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!gatewayRows.length" key="empty" class="row-static">
              <td colspan="7" class="text-ink-muted">
                No gateways. The default route from the interface is used as it is.
              </td>
            </tr>
            <tr
              v-for="g in gatewayRows"
              :key="g.name"
              :class="{
                'opacity-50': !g.enabled,
                'row-changed': config.isChanged('gateways', g.name),
              }"
            >
              <td>
                <div class="font-mono font-medium">{{ g.name }}</div>
                <div class="text-xs text-ink-muted">{{ g.description }}</div>
              </td>
              <td class="font-mono text-code">{{ g.interface }}</td>
              <td class="font-mono text-code">
                {{ g.live?.address || g.address || 'from DHCP' }}
                <div v-if="detectedFor(g)" class="text-xs text-ink-muted">
                  kernel: {{ detectedFor(g).address }} · metric {{ detectedFor(g).metric }} ·
                  {{ detectedFor(g).protocol }}
                </div>
              </td>
              <td class="font-mono text-code">{{ g.monitor || 'the gateway' }}</td>
              <td class="font-mono text-code">{{ g.priority ?? 0 }}</td>
              <td class="whitespace-nowrap">
                <template v-if="g.live && !g.live.unknown">
                  <span class="badge" :class="g.live.online ? 'badge-ok' : 'badge-warn'">
                    {{ g.live.online ? 'up' : 'down' }}
                  </span>
                  <span v-if="g.live.active" class="badge badge-ok ml-1">active</span>
                  <div class="mt-1 font-mono text-xs text-ink-muted">
                    {{ g.live.latencyMs.toFixed(1) }}ms · {{ g.live.lossPercent.toFixed(0) }}% loss
                  </div>
                </template>
                <span v-else class="badge">not probed</span>
              </td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editGateway(g)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete gateway ${g.name}?`"
                  :description="g.description"
                  :dependents="config.gatewayDependents(g.name)"
                  dependents-label="Also changed"
                  @confirm="config.removeGateway(g.name)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </SectionCard>

      <SectionCard
        title="Detected routes"
        :count="detected.length"
        intro="The default routes this router has right now, whether Ostiole put them there or not."
        flush
      >
        <table class="table">
          <thead>
            <tr>
              <th>Next hop</th>
              <th>Interface</th>
              <th>Family</th>
              <th>Metric</th>
              <th>From</th>
              <th>Gateway</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!detected.length" key="empty" class="row-static">
              <td colspan="7" class="text-ink-muted">
                {{ refresh.updatedAt.value ? 'No default route on this router.' : 'Reading…' }}
              </td>
            </tr>
            <tr v-for="d in detected" :key="`${d.interface}-${d.address}-${d.family}`">
              <td class="font-mono text-code">{{ d.address }}</td>
              <td class="font-mono text-code">{{ d.interface }}</td>
              <td>{{ d.family }}</td>
              <td class="font-mono text-code">{{ d.metric }}</td>
              <td>{{ d.protocol }}</td>
              <td>
                <span v-if="coveredBy(d)" class="font-mono text-code text-ink-muted">
                  {{ coveredBy(d) }}
                </span>
                <span v-else class="badge">not watched</span>
              </td>
              <td class="text-right whitespace-nowrap">
                <button
                  v-if="!coveredBy(d) && d.suggested"
                  type="button"
                  class="link"
                  @click="adopt(d)"
                >
                  Add as {{ d.suggested.name }}
                </button>
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </SectionCard>

      <SectionCard title="Gateway groups" :count="groupRows.length" flush>
        <template #actions>
          <button
            type="button"
            class="btn-secondary"
            :disabled="!config.gateways.length"
            @click="addGroup"
          >
            <Plus class="size-4" aria-hidden="true" /> Add group
          </button>
        </template>
        <table class="table">
          <thead>
            <tr>
              <th>Group</th>
              <th>Members</th>
              <th>When all are down</th>
              <th>Rules</th>
              <th>Next hop</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!groupRows.length" key="empty" class="row-static">
              <td colspan="6" class="text-ink-muted">
                <template v-if="config.gateways.length">
                  No groups. Add one to route some rules over a different line.
                </template>
                <template v-else> Add a gateway first. A group is made of them. </template>
              </td>
            </tr>
            <tr
              v-for="g in groupRows"
              :key="g.name"
              :class="{
                'opacity-50': !g.enabled,
                'row-changed': config.isChanged('gatewayGroups', g.name),
              }"
            >
              <td>
                <div class="font-mono font-medium">{{ g.name }}</div>
                <div class="text-xs text-ink-muted">{{ g.description }}</div>
              </td>
              <td class="font-mono text-code">{{ tierSummary(g) || '—' }}</td>
              <td>
                <span v-if="g.onDown === 'block'" class="badge badge-warn">drop</span>
                <span v-else class="text-ink-muted">default route</span>
              </td>
              <td class="font-mono text-code">{{ g.policy?.rules ?? 0 }}</td>
              <td class="font-mono text-code">
                <template v-if="g.policy?.nextHops?.length">
                  {{ g.policy.nextHops.join(', ') }}
                </template>
                <span v-else-if="g.onDown === 'block'" class="badge badge-warn"
                  >blocking traffic</span
                >
                <span v-else class="text-ink-muted">default route</span>
              </td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editGroup(g)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete group ${g.name}?`"
                  :description="g.description"
                  :dependents="config.groupDependents(g.name)"
                  dependents-label="Also changed"
                  @confirm="config.removeGatewayGroup(g.name)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
        <div v-if="policyRules.length" class="border-t border-line px-4 py-3 text-ink-muted">
          <span class="font-medium">Policy routing is in use:</span>
          <template v-for="(r, i) in policyRules" :key="r.id">
            <span v-if="i">,&nbsp;</span>
            <span v-else>&nbsp;</span>
            <RouterLink :to="`/firewall/rules#${r.zone}`" class="link">{{ r.id }}</RouterLink>
            → {{ r.gateway }}
          </template>
        </div>
      </SectionCard>

      <SectionCard title="Static routes" :count="config.routes.length" flush>
        <template #actions>
          <button type="button" class="btn-secondary" @click="add">
            <Plus class="size-4" aria-hidden="true" /> Add static route
          </button>
        </template>
        <table class="table">
          <thead>
            <tr>
              <th>Destination</th>
              <th>Gateway</th>
              <th>Interface</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="config.routes.length === 0" key="empty" class="row-static">
              <td colspan="5" class="text-ink-muted">No static routes.</td>
            </tr>
            <tr
              v-for="r in config.routes"
              :key="r.id"
              :class="{ 'opacity-50': !r.enabled, 'row-changed': config.isChanged('routes', r.id) }"
            >
              <td class="font-mono text-code">{{ r.destination }}</td>
              <td class="font-mono text-code">{{ r.gateway }}</td>
              <td class="font-mono text-code">{{ r.interface ?? 'auto' }}</td>
              <td>{{ r.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="edit(r)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete route ${r.id}?`"
                  :description="r.description"
                  @confirm="config.removeRoute(r.id)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </SectionCard>
    </template>

    <GatewayDialog v-model:open="gwOpen" :gateway="gwEditing" />
    <GatewayGroupDialog v-model:open="groupOpen" :group="groupEditing" />

    <StaticRouteDialog v-model:open="open" :route="editing" />
  </div>
</template>
