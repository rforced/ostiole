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
 * Nothing under the header shows until the first overview is in. Its
 * warnings go above the interfaces and the cards it adds go between the
 * others, so a page drawn before it would reshuffle when it came. A read
 * that fails shows what there is.
 */
const ready = computed(() => loaded.value || Boolean(load.error.value))

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

// Nothing here needs another's answer, so the reads start together; the
// overview is the slow one.
onMounted(() => {
  readHealth()
  load.run()
  loadStats.run()
  readUpdate()
})
</script>

<template>
  <div class="space-y-5">
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

    <template v-if="ready">
      <DashboardWarnings :warnings="overview?.warnings ?? []" />

      <InterfaceSummary :interfaces="overview?.interfaces ?? []" :rates="rates" :loaded="loaded" />

      <!-- The cards every router has are always here. The rest only exist
           once the overview says there is something in them. -->
      <div class="grid gap-4 lg:grid-cols-2">
        <SystemLoadCard :stats="stats" :loaded="statsLoaded" />
        <GatewaysCard
          v-if="overview?.gateways?.length || overview?.unwatchedGateways?.length"
          :gateways="overview.gateways ?? []"
          :unwatched="overview.unwatchedGateways ?? []"
        />
        <TopRulesCard
          :rules="overview?.topRules ?? []"
          :blocked="overview?.blocked"
          :loaded="loaded"
        />
        <RecentBlocksCard v-if="overview?.recentBlocks" :blocks="overview.recentBlocks" />
        <ServicesCard
          :services="overview?.services ?? []"
          :dhcp="overview?.dhcp ?? {}"
          :dns="overview?.dns ?? {}"
          :loaded="loaded"
        />
        <RecentLeasesCard
          v-if="overview?.dhcp?.enabled"
          :leases="overview.recentLeases ?? []"
          :total="overview.dhcp.leases ?? 0"
        />
        <WirelessCard v-if="overview?.wireless" :wireless="overview.wireless" />
        <BlockingCard v-if="overview?.blocking?.enabled" :blocking="overview.blocking" />
        <RouterCard
          :status="status"
          :summary="overview?.status ?? {}"
          :health="health"
          :update="update"
          :loaded="loaded"
        />
      </div>
    </template>
    <p v-else class="sr-only">Reading…</p>
  </div>
</template>
