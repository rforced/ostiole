<script setup lang="ts">
import { LayoutGrid } from '@lucide/vue'
import { computed, onMounted, ref, watch } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import PageHeader from '@/components/PageHeader.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { createRateTracker } from '@/lib/rates'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'
import DashboardWarnings from '@/views/dashboard/DashboardWarnings.vue'
import { loadLayout, saveLayout } from '@/views/dashboard/layout'
import {
  OPTIONAL,
  externalLinks,
  heightsIn,
  rememberedShape,
  rememberShape,
  shapeOf,
} from '@/views/dashboard/shape'
import BlockingCard from '@/views/dashboard/BlockingCard.vue'
import GatewaysCard from '@/views/dashboard/GatewaysCard.vue'
import InterfaceSummary from '@/views/dashboard/InterfaceSummary.vue'
import LayoutDialog from '@/views/dashboard/LayoutDialog.vue'
import RecentBlocksCard from '@/views/dashboard/RecentBlocksCard.vue'
import RecentLeasesCard from '@/views/dashboard/RecentLeasesCard.vue'
import RouterCard from '@/views/dashboard/RouterCard.vue'
import ServicesCard from '@/views/dashboard/ServicesCard.vue'
import SystemLoadCard from '@/views/dashboard/SystemLoadCard.vue'
import TopRulesCard from '@/views/dashboard/TopRulesCard.vue'
import TrafficCard from '@/views/dashboard/TrafficCard.vue'
import WirelessCard from '@/views/dashboard/WirelessCard.vue'

/** Live enough for counters and carrier, quiet enough for a router. */
const REFRESH_MS = 10_000
/**
 * Usage is sampled faster, because a meter that only moves every ten
 * seconds does not read as live. It is two small file reads on the router.
 */
const STATS_MS = 3_000

const auth = useAuthStore()
const config = useConfigStore()
const system = useSystemStore()
const health = ref(null)
const overview = ref(null)
const stats = ref(null)
const healthError = ref('')
const update = ref(null)
const status = computed(() => system.status)
/** Bits per second per interface, from the last two overviews. */
const rates = ref({})
const sampleRates = createRateTracker()

const load = useAsync(
  async () => {
    const [ov] = await Promise.all([api.overview(), system.refresh()])
    overview.value = ov
    rates.value = sampleRates(overview.value.interfaces ?? [])
  },
  { interval: REFRESH_MS },
)

/** Usage failing is never worth an error banner over the whole dashboard. */
const loadStats = useAsync(
  async () => {
    try {
      stats.value = await api.systemStats()
    } catch {
      stats.value = null
    }
  },
  { interval: STATS_MS, autostart: false },
)

/** Each gateway's last day; empty where the router keeps none. */
const strips = ref(null)
const loadStrips = useAsync(
  async () => {
    try {
      strips.value = await api.gatewayHistory.strips()
    } catch {
      strips.value = []
    }
  },
  { interval: 60_000, autostart: false },
)

const error = computed(() => healthError.value || load.error.value || system.error)
/** Until the first overview arrives, an empty list means not read yet. */
const loaded = computed(() => load.updatedAt.value > 0)
/** Whether the first reads have all answered, with what they asked for or without. */
const answered = ref(false)
/** Whether the Traffic card's first read has answered. */
const trafficRead = ref(false)
/**
 * Which cards the page has and how many rows each holds. Until the first
 * overview is in, it is the shape of the last one this browser saw: an
 * overview's warnings go above the interfaces and the cards it adds go
 * between the others, so placeholders of any other shape would reshuffle
 * the page when it came.
 */
const remembered = rememberedShape()
const shape = computed(() =>
  overview.value ? { ...remembered, ...shapeOf(overview.value) } : remembered,
)
/** The order of the grid's cards and which of them this browser hides. */
const layout = ref(loadLayout())
const layoutOpen = ref(false)

function setLayout(next) {
  layout.value = next
  saveLayout(next)
}

const hidden = (card) => layout.value.hidden.includes(card)
/**
 * Whether the page draws a card. One that comes and goes needs the
 * overview to have something for it.
 */
const shown = (card) =>
  (!OPTIONAL.includes(card) || Object.hasOwn(shape.value.cards, card)) && !hidden(card)
/** The grid's cards in this browser's order. */
const grid = computed(() => layout.value.order.filter(shown))

/**
 * The polls of a card's own, which stop while the card is hidden. They go
 * by the layout alone, so a first visit still reads the strips with the
 * rest, before the overview says there are gateways.
 */
const polls = { system: loadStats, gateways: loadStrips }

/** Reads a card's own data and polls it, unless the card is hidden. */
function follow(card) {
  const read = polls[card]
  if (hidden(card)) {
    read.stop()
    return undefined
  }
  read.start()
  return read.run()
}
for (const card of Object.keys(polls)) {
  watch(
    () => hidden(card),
    () => follow(card),
  )
}

/**
 * Every card holds its placeholder until all of them can be drawn, so the
 * page fills in at once rather than a card at a time: the overview, the
 * usage, health, the update check and the Traffic card's charts. The
 * overview has to arrive, the rest only to answer. Once in, it stays in,
 * and a card that turns up later reads on its own.
 */
const ready = ref(false)
watch(
  () => answered.value && loaded.value && (trafficRead.value || !shown('traffic')),
  (all) => {
    if (all) ready.value = true
  },
)
/** The overview the cards draw, none until then. */
const drawn = computed(() => (ready.value ? overview.value : null))

/**
 * A placeholder holds the room its card took last time, so a row with
 * more lines than a placeholder has, or a note that wraps, moves nothing
 * when it arrives.
 */
function hold(card) {
  const h = shape.value.heights[card]
  return !ready.value && h ? { minHeight: `${h}px` } : undefined
}

// Each overview, once drawn, is the shape the next visit starts from.
const page = ref(null)
watch(
  [load.updatedAt, ready],
  () => {
    if (ready.value && page.value) {
      rememberShape({ ...shapeOf(overview.value), heights: heightsIn(page.value) })
    }
  },
  { flush: 'post' },
)

// An apply or a revert changes what the router is doing.
watch(
  () => config.applied,
  () => load.run(),
)

async function readHealth() {
  try {
    health.value = await api.health()
  } catch (e) {
    healthError.value = errorMessage(e)
  }
}

/**
 * Best effort: a quiet hint when a newer release exists. It comes from
 * what the router's own nightly check wrote down — on the channel the
 * configuration names — so opening the dashboard costs GitHub nothing.
 */
async function readUpdate() {
  try {
    const res = (await api.update.status()) as { check?: { available?: boolean } }
    if (res.check?.available) update.value = res.check
  } catch {
    /* offline or updates unavailable */
  }
}

// Nothing here needs another's answer, so all go at once; none throws. The
// overview is the slow one, and the cards wait for it together.
onMounted(async () => {
  await Promise.all([readHealth(), load.run(), follow('system'), readUpdate(), follow('gateways')])
  answered.value = true
})
</script>

<template>
  <div ref="page" class="space-y-5">
    <PageHeader title="Dashboard">
      <button type="button" class="btn-secondary" @click="layoutOpen = true">
        <LayoutGrid class="size-4" aria-hidden="true" />
        Layout
      </button>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </PageHeader>
    <ErrorLine v-if="error" class="text-sm">
      {{ error }}
    </ErrorLine>

    <AppNotice v-if="status && !status.configured" kind="info">
      This router has no configuration yet.
      <template v-if="!auth.readOnly">
        <RouterLink to="/wizard" class="link">Run the setup wizard</RouterLink>.
      </template>
    </AppNotice>

    <DashboardWarnings
      data-card="warnings"
      :style="hold('warnings')"
      :warnings="drawn?.warnings ?? []"
      :loaded="ready"
      :placeholders="shape.warnings"
    />

    <InterfaceSummary
      data-card="interfaces"
      :style="hold('interfaces')"
      :interfaces="drawn?.interfaces ?? []"
      :rates="rates"
      :loaded="ready"
      :placeholders="shape.interfaces"
    />

    <!-- The cards every router has are always here unless hidden. The rest
         exist when the overview has something for them, and until it is
         in, when the last one did. -->
    <div class="grid gap-4 lg:grid-cols-2">
      <template v-for="card in grid" :key="card">
        <SystemLoadCard
          v-if="card === 'system'"
          data-card="system"
          :style="hold('system')"
          :stats="ready ? stats : null"
          :loaded="ready"
        />
        <GatewaysCard
          v-else-if="card === 'gateways'"
          data-card="gateways"
          :style="hold('gateways')"
          :gateways="drawn?.gateways ?? []"
          :unwatched="drawn?.unwatchedGateways ?? []"
          :strips="ready ? strips : null"
          :loaded="ready"
          :placeholders="shape.cards.gateways"
        />
        <TrafficCard
          v-else-if="card === 'traffic'"
          data-card="traffic"
          :style="hold('traffic')"
          :links="drawn ? externalLinks(drawn) : []"
          :loaded="ready"
          :placeholders="shape.cards.traffic"
          @read="trafficRead = true"
        />
        <TopRulesCard
          v-else-if="card === 'rules'"
          data-card="rules"
          :style="hold('rules')"
          :rules="drawn?.topRules ?? []"
          :blocked="drawn?.blocked"
          :loaded="ready"
          :placeholders="shape.rules"
        />
        <RecentBlocksCard
          v-else-if="card === 'blocks'"
          data-card="blocks"
          :style="hold('blocks')"
          :blocks="drawn?.recentBlocks ?? []"
          :loaded="ready"
          :placeholders="shape.cards.blocks"
        />
        <ServicesCard
          v-else-if="card === 'services'"
          data-card="services"
          :style="hold('services')"
          :services="drawn?.services ?? []"
          :dhcp="drawn?.dhcp ?? {}"
          :dns="drawn?.dns ?? {}"
          :loaded="ready"
          :placeholders="shape.services"
        />
        <RecentLeasesCard
          v-else-if="card === 'leases'"
          data-card="leases"
          :style="hold('leases')"
          :leases="drawn?.recentLeases ?? []"
          :total="drawn?.dhcp?.leases ?? 0"
          :loaded="ready"
          :placeholders="shape.cards.leases"
        />
        <WirelessCard
          v-else-if="card === 'wireless'"
          data-card="wireless"
          :style="hold('wireless')"
          :wireless="drawn?.wireless"
          :loaded="ready"
          :placeholders="shape.cards.wireless"
        />
        <BlockingCard
          v-else-if="card === 'blocking'"
          data-card="blocking"
          :style="hold('blocking')"
          :blocking="drawn?.blocking"
          :loaded="ready"
        />
        <RouterCard
          v-else-if="card === 'router'"
          data-card="router"
          :style="hold('router')"
          :status="status"
          :summary="drawn?.status ?? {}"
          :health="health"
          :update="update"
          :loaded="ready"
        />
      </template>
    </div>

    <LayoutDialog v-model:open="layoutOpen" :layout="layout" @change="setLayout" />
  </div>
</template>
