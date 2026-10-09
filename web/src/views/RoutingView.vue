<script setup>
import { Plus } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import PageHeader from '@/components/PageHeader.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { tone } from '@/lib/badge'
import { neverHint, stateOf } from '@/lib/gateways'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import GatewayDialog from '@/views/routing/GatewayDialog.vue'
import GatewayEvents from '@/views/routing/GatewayEvents.vue'
import GatewayGroupDialog from '@/views/routing/GatewayGroupDialog.vue'
import GatewayHistoryDialog from '@/views/routing/GatewayHistoryDialog.vue'
import StaticRouteDialog from '@/views/routing/StaticRouteDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const open = ref(false)
const editing = ref(null)
const gwOpen = ref(false)
const gwEditing = ref(null)
const groupOpen = ref(false)
const groupEditing = ref(null)
const historyOpen = ref(false)
const historyOf = ref(null)
const live = ref([])
const policy = ref([])
const replies = ref([])
const detected = ref([])

/** Configured gateways merged with what the monitor sees. */
const gatewayRows = computed(() =>
  config.gateways.map((g) => ({
    ...g,
    live: live.value.find((l) => l.name === g.name) ?? null,
    // No address on a tunnel: rules send traffic into it, and it never
    // carries the default route.
    tunnel: !g.address && Boolean(config.interfaces.find((i) => i.name === g.interface)?.wireguard),
  })),
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
      replies.value = await api.policyReplies()
    } catch {
      replies.value = []
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
  const g = config.gateways.find((g) => g.enabled && claims(g, d))
  return g?.name ?? ''
}

/** A disabled gateway that would cover a detected route, which it then names. */
function disabledFor(d) {
  if (coveredBy(d)) return ''
  return config.gateways.find((g) => !g.enabled && claims(g, d))?.name ?? ''
}

function claims(g, d) {
  return g.interface === d.interface && (!g.address || g.address === d.address)
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
  if (!g.enabled) return null
  return detected.value.find((d) => d.configured === g.name || claims(g, d)) ?? null
}

function showHistory(g) {
  historyOf.value = g
  historyOpen.value = true
}

/** Empties every gateway's latency and loss, their files included. */
const clearHistory = useAsync(() => api.gatewayHistory.clear())

/** A gateway's figures, a line for each family when it has two. */
function figureLines(live) {
  const fams = live.families ?? []
  const line = (f) => `${f.latencyMs.toFixed(1)}ms · ${f.lossPercent.toFixed(0)}% loss`
  if (fams.length < 2) return [line(live)]
  return fams.map((f) =>
    f.online || f.unknown ? `${f.family} ${line(f)}` : `${f.family} ${stateOf(f)}`,
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
    <ErrorLine v-if="config.error" class="text-sm">
      {{ config.error }}
    </ErrorLine>
    <p v-if="config.loaded && !config.draft" class="text-sm text-ink-muted">
      No configuration yet.
      <template v-if="!auth.readOnly">
        <RouterLink to="/wizard" class="link">Run the setup wizard</RouterLink> first.
      </template>
    </p>
    <template v-else-if="config.draft">
      <SectionCard
        title="Gateways"
        :count="gatewayRows.length"
        intro="Each one is probed, and the default route moves off a gateway that stops answering."
        flush
      >
        <template v-if="!auth.readOnly" #actions>
          <ClearLogButton
            name="gateway history"
            :description="
              config.saved?.system?.logging?.files?.enabled
                ? 'Every gateway\'s latency and loss is dropped, and its files are deleted. The events stay.'
                : 'Every gateway\'s latency and loss is dropped. The events stay.'
            "
            :busy="clearHistory.busy.value"
            @confirm="clearHistory.run()"
          />
          <button type="button" class="btn-secondary" @click="addGateway">
            <Plus class="size-4" aria-hidden="true" /> Add gateway
          </button>
        </template>
        <ErrorLine v-if="clearHistory.error.value" class="card-strip">
          {{ clearHistory.error.value }}
        </ErrorLine>
        <table class="table table-stack">
          <thead>
            <tr>
              <th>Gateway</th>
              <th>Interface</th>
              <th>Address</th>
              <th>Monitor</th>
              <th class="num">Priority</th>
              <th>State</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!gatewayRows.length">
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
              <td data-label="">
                <div class="font-mono font-medium">{{ g.name }}</div>
                <div class="text-xs text-ink-muted">{{ g.description }}</div>
              </td>
              <td class="font-mono text-code" data-label="Interface">{{ g.interface }}</td>
              <td class="font-mono text-code" data-label="Address">
                <span v-if="g.tunnel" class="font-sans">through the tunnel</span>
                <template v-else>{{ g.live?.address || g.address || 'from DHCP' }}</template>
                <div v-if="detectedFor(g)" class="text-xs text-ink-muted">
                  kernel: {{ detectedFor(g).address }} · metric {{ detectedFor(g).metric }} ·
                  {{ detectedFor(g).protocol
                  }}<template v-if="detectedFor(g).demoted"> · demoted</template>
                </div>
              </td>
              <td class="font-mono text-code" data-label="Monitor">
                {{ g.monitor || 'the next hop' }}
              </td>
              <td class="num font-mono text-code" data-label="Priority">
                {{ g.tunnel ? '—' : (g.priority ?? 0) }}
              </td>
              <td class="whitespace-nowrap" data-label="State">
                <template v-if="g.live && !g.live.unknown">
                  <span class="inline-flex items-center gap-1.5">
                    <span class="badge" :class="tone(stateOf(g.live))">
                      {{ stateOf(g.live) }}
                    </span>
                    <span v-if="g.live.active" class="badge badge-ok">active</span>
                    <span v-if="g.live.slow?.length" class="badge badge-warn">slow</span>
                    <span v-if="g.live.lossy?.length" class="badge badge-warn">
                      losing packets
                    </span>
                  </span>
                  <template v-if="g.live.neverAnswered">
                    <div class="mt-1 max-w-48 text-xs whitespace-normal text-ink-muted">
                      {{ neverHint(g) }}
                    </div>
                  </template>
                  <template v-else>
                    <div
                      v-for="line in figureLines(g.live)"
                      :key="line"
                      class="mt-1 font-mono text-xs text-ink-muted"
                    >
                      {{ line }}
                    </div>
                  </template>
                </template>
                <span v-else-if="g.live" class="badge">probing</span>
                <span v-else class="badge">not probed</span>
              </td>
              <td class="actions" data-label="">
                <button type="button" class="link-action" @click="showHistory(g)">History</button>
                <button type="button" class="link-action" @click="editGateway(g)">
                  {{ auth.readOnly ? 'View' : 'Edit' }}
                </button>
                <ConfirmButton
                  label="Delete"
                  :question="`Delete gateway ${g.name}?`"
                  :description="g.description"
                  :dependents="config.gatewayDependents(g.name)"
                  dependents-label="Also changed"
                  @confirm="config.removeGateway(g.name)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </SectionCard>

      <GatewayEvents />

      <SectionCard
        title="Detected routes"
        :count="detected.length"
        intro="The default routes this router has right now, whether Ostiole put them there or not."
        flush
      >
        <table class="table table-stack">
          <thead>
            <tr>
              <th>Next hop</th>
              <th>Interface</th>
              <th>Family</th>
              <th class="num">Metric</th>
              <th>From</th>
              <th>Gateway</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!detected.length">
              <td colspan="7" class="text-ink-muted">
                {{ emptyText(refresh, 'No default routes.') }}
              </td>
            </tr>
            <tr v-for="d in detected" :key="`${d.interface}-${d.address}-${d.family}-${d.metric}`">
              <td class="font-mono text-code" data-label="">{{ d.address }}</td>
              <td class="font-mono text-code" data-label="Interface">{{ d.interface }}</td>
              <td data-label="Family">{{ d.family }}</td>
              <td class="num font-mono text-code" data-label="Metric">
                <span class="inline-flex items-center gap-1.5">
                  {{ d.metric }}
                  <span v-if="d.demoted" class="badge badge-warn font-sans">demoted</span>
                </span>
              </td>
              <td data-label="From">{{ d.protocol }}</td>
              <td data-label="Gateway">
                <span v-if="coveredBy(d)" class="font-mono text-code text-ink-muted">
                  {{ coveredBy(d) }}
                </span>
                <template v-else>
                  <span class="badge">not watched</span>
                  <div class="text-xs text-ink-muted">
                    <span v-if="disabledFor(d)" class="font-mono text-code"
                      >{{ disabledFor(d) }}, disabled.
                    </span>
                    Keeps no history.
                  </div>
                </template>
              </td>
              <td class="actions" data-label="">
                <button
                  v-if="!coveredBy(d) && !disabledFor(d) && d.suggested && !auth.readOnly"
                  type="button"
                  class="link-action"
                  @click="adopt(d)"
                >
                  Add as {{ d.suggested.name }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </SectionCard>

      <SectionCard title="Gateway groups" :count="groupRows.length" flush>
        <template v-if="!auth.readOnly" #actions>
          <button
            type="button"
            class="btn-secondary"
            :disabled="!config.gateways.length"
            @click="addGroup"
          >
            <Plus class="size-4" aria-hidden="true" /> Add group
          </button>
        </template>
        <table class="table table-stack">
          <thead>
            <tr>
              <th>Group</th>
              <th>Members</th>
              <th>When all are down</th>
              <th class="num">Rules</th>
              <th>Next hop</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!groupRows.length">
              <td colspan="6" class="text-ink-muted">
                <template v-if="config.gateways.length">
                  No groups. Add one to route some rules over a different line.
                </template>
                <template v-else> No groups. Add a gateway first. </template>
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
              <td data-label="">
                <div class="font-mono font-medium">{{ g.name }}</div>
                <div class="text-xs text-ink-muted">{{ g.description }}</div>
              </td>
              <td class="font-mono text-code" data-label="Members">{{ tierSummary(g) || '—' }}</td>
              <td data-label="All down">
                <span v-if="g.onDown === 'block'" class="badge badge-warn">drop</span>
                <span v-else class="text-ink-muted">default route</span>
              </td>
              <td class="num font-mono text-code" data-label="Rules">{{ g.policy?.rules ?? 0 }}</td>
              <td class="font-mono text-code" data-label="Next hop">
                <template v-if="g.policy?.nextHops?.length">
                  {{ g.policy.nextHops.join(', ') }}
                </template>
                <span v-else-if="g.onDown === 'block'" class="badge badge-warn"
                  >blocking traffic</span
                >
                <span v-else class="text-ink-muted">default route</span>
              </td>
              <td class="actions" data-label="">
                <button type="button" class="link-action" @click="editGroup(g)">
                  {{ auth.readOnly ? 'View' : 'Edit' }}
                </button>
                <ConfirmButton
                  label="Delete"
                  :question="`Delete group ${g.name}?`"
                  :description="g.description"
                  :dependents="config.groupDependents(g.name)"
                  dependents-label="Also changed"
                  @confirm="config.removeGatewayGroup(g.name)"
                />
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="policyRules.length" class="card-strip border-t border-line text-ink-muted">
          <span class="font-medium">Policy routing is in use:</span>
          <template v-for="(r, i) in policyRules" :key="r.id">
            <span v-if="i">,&nbsp;</span>
            <span v-else>&nbsp;</span>
            <RouterLink :to="`/firewall/rules#${r.zone}`" class="link">{{ r.id }}</RouterLink>
            → {{ r.gateway }}
          </template>
        </p>
      </SectionCard>

      <SectionCard
        v-if="replies.length"
        title="Replies"
        :count="replies.length"
        intro="Connections that come in on an external interface are answered out of it, whichever holds the default route."
        flush
      >
        <table class="table table-stack">
          <thead>
            <tr>
              <th>Interface</th>
              <th>Table</th>
              <th>Next hop</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in replies" :key="r.interface">
              <td class="font-mono text-code" data-label="">{{ r.interface }}</td>
              <td class="font-mono text-code" data-label="Table">{{ r.table }}</td>
              <td class="font-mono text-code" data-label="Next hop">
                <template v-if="r.nextHops.length">{{ r.nextHops.join(', ') }}</template>
                <span v-else class="font-sans text-ink-muted">default route</span>
              </td>
            </tr>
          </tbody>
        </table>
      </SectionCard>

      <SectionCard title="Static routes" :count="config.routes.length" flush>
        <template v-if="!auth.readOnly" #actions>
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
          <tbody>
            <tr v-if="config.routes.length === 0">
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
              <td class="actions">
                <button type="button" class="link-action" @click="edit(r)">
                  {{ auth.readOnly ? 'View' : 'Edit' }}
                </button>
                <ConfirmButton
                  label="Delete"
                  :question="`Delete route ${r.id}?`"
                  :description="r.description"
                  @confirm="config.removeRoute(r.id)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </SectionCard>
    </template>

    <GatewayDialog v-model:open="gwOpen" :gateway="gwEditing" />
    <GatewayHistoryDialog v-model:open="historyOpen" :gateway="historyOf" />
    <GatewayGroupDialog v-model:open="groupOpen" :group="groupEditing" />

    <StaticRouteDialog v-model:open="open" :route="editing" />
  </div>
</template>
