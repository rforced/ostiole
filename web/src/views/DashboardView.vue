<script setup>
import { computed, onMounted, ref, watch } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import PageHeader from '@/components/PageHeader.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { createRateTracker } from '@/lib/rates'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'
import DashboardWarnings from '@/views/dashboard/DashboardWarnings.vue'
import { heightsIn, rememberedShape, rememberShape, shapeOf } from '@/views/dashboard/shape'
import BlockingCard from '@/views/dashboard/BlockingCard.vue'
import GatewaysCard from '@/views/dashboard/GatewaysCard.vue'
import InterfaceSummary from '@/views/dashboard/InterfaceSummary.vue'
import RecentBlocksCard from '@/views/dashboard/RecentBlocksCard.vue'
import RecentLeasesCard from '@/views/dashboard/RecentLeasesCard.vue'
import RouterCard from '@/views/dashboard/RouterCard.vue'
import ServicesCard from '@/views/dashboard/ServicesCard.vue'
import SystemLoadCard from '@/views/dashboard/SystemLoadCard.vue'
import TopRulesCard from '@/views/dashboard/TopRulesCard.vue'
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
    rates.value = sampleRates(ov.interfaces ?? [])
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
  { interval: STATS_MS },
)

const error = computed(() => healthError.value || load.error.value || system.error)
/** Until the first overview arrives, an empty list means not read yet. */
const loaded = computed(() => load.updatedAt.value > 0)
/** The usage read swallows its errors, so this is only whether it has answered. */
const statsLoaded = computed(() => loadStats.updatedAt.value > 0)
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
/** Whether a card that comes and goes is on the page. */
const has = (card) => Object.hasOwn(shape.value.cards, card)

/**
 * A placeholder holds the room its card took last time, so a row with
 * more lines than a placeholder has, or a note that wraps, moves nothing
 * when it arrives.
 */
function hold(card, ready = loaded.value) {
  const h = shape.value.heights[card]
  return !ready && h ? { minHeight: `${h}px` } : undefined
}

// Each overview, once drawn, is the shape the next visit starts from.
const page = ref(null)
watch(
  load.updatedAt,
  () => {
    if (overview.value && page.value) {
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
    const res = await api.update.status()
    if (res.check?.available) update.value = res.check
  } catch {
    /* offline or updates unavailable */
  }
}

// Nothing here needs another's answer. The overview is the slow one, and
// the usage meters do not wait for it.
onMounted(() => {
  readHealth()
  load.run()
  loadStats.run()
  readUpdate()
})
</script>

<template>
  <div ref="page" class="space-y-5">
    <PageHeader title="Dashboard">
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </PageHeader>
    <p v-if="error" role="alert" class="text-sm text-bad">
      {{ error }}
    </p>

    <AppNotice v-if="status && !status.configured" kind="info">
      This firewall has no configuration yet.
      <template v-if="!auth.readOnly">
        <RouterLink to="/wizard" class="font-medium underline">Run the setup wizard</RouterLink>.
      </template>
    </AppNotice>

    <DashboardWarnings
      data-card="warnings"
      :style="hold('warnings')"
      :warnings="overview?.warnings ?? []"
      :loaded="loaded"
      :placeholders="shape.warnings"
    />

    <InterfaceSummary
      data-card="interfaces"
      :style="hold('interfaces')"
      :interfaces="overview?.interfaces ?? []"
      :rates="rates"
      :loaded="loaded"
      :placeholders="shape.interfaces"
    />

    <!-- The cards every router has are always here. The rest exist when the
         overview has something for them, and until it is in, when the last
         one did. -->
    <div class="grid gap-4 lg:grid-cols-2">
      <SystemLoadCard
        data-card="system"
        :style="hold('system', statsLoaded)"
        :stats="stats"
        :loaded="statsLoaded"
      />
      <GatewaysCard
        v-if="has('gateways')"
        data-card="gateways"
        :style="hold('gateways')"
        :gateways="overview?.gateways ?? []"
        :unwatched="overview?.unwatchedGateways ?? []"
        :loaded="loaded"
        :placeholders="shape.cards.gateways"
      />
      <TopRulesCard
        data-card="rules"
        :style="hold('rules')"
        :rules="overview?.topRules ?? []"
        :blocked="overview?.blocked"
        :loaded="loaded"
        :placeholders="shape.rules"
      />
      <RecentBlocksCard
        v-if="has('blocks')"
        data-card="blocks"
        :style="hold('blocks')"
        :blocks="overview?.recentBlocks ?? []"
        :loaded="loaded"
        :placeholders="shape.cards.blocks"
      />
      <ServicesCard
        data-card="services"
        :style="hold('services')"
        :services="overview?.services ?? []"
        :dhcp="overview?.dhcp ?? {}"
        :dns="overview?.dns ?? {}"
        :loaded="loaded"
        :placeholders="shape.services"
      />
      <RecentLeasesCard
        v-if="has('leases')"
        data-card="leases"
        :style="hold('leases')"
        :leases="overview?.recentLeases ?? []"
        :total="overview?.dhcp?.leases ?? 0"
        :loaded="loaded"
        :placeholders="shape.cards.leases"
      />
      <WirelessCard
        v-if="has('wireless')"
        data-card="wireless"
        :style="hold('wireless')"
        :wireless="overview?.wireless"
        :loaded="loaded"
        :placeholders="shape.cards.wireless"
      />
      <BlockingCard
        v-if="has('blocking')"
        data-card="blocking"
        :style="hold('blocking')"
        :blocking="overview?.blocking"
        :loaded="loaded"
      />
      <RouterCard
        data-card="router"
        :style="hold('router')"
        :status="status"
        :summary="overview?.status ?? {}"
        :health="health"
        :update="update"
        :loaded="loaded"
      />
    </div>
  </div>
</template>
