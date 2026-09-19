<script setup>
import { computed, onMounted, ref } from 'vue'

import ApplyPending from '@/components/ApplyPending.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { createRateTracker } from '@/lib/rates'
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

onMounted(async () => {
  try {
    health.value = await api.health()
  } catch (e) {
    healthError.value = errorMessage(e)
  }
  await load.run()
  await loadStats.run()
  // Best effort: a quiet hint when a newer release exists. It comes from
  // what the router's own nightly check wrote down — on the channel the
  // configuration names — so opening the dashboard costs GitHub nothing.
  try {
    const res = await api.update.status()
    if (res.check?.available) update.value = res.check
  } catch {
    /* offline or updates unavailable */
  }
})
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between gap-4">
      <h1 class="text-2xl font-semibold tracking-tight">Dashboard</h1>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ error }}
    </p>

    <ApplyPending
      v-if="status?.pending"
      :key="status.pending.deadline"
      :deadline="status.pending.deadline"
      @confirmed="load.run()"
      @reverted="load.run()"
    />

    <p
      v-if="status && !status.configured"
      class="rounded-lg border border-sky-300 bg-sky-50 p-4 text-sm dark:border-sky-800 dark:bg-sky-950/40"
    >
      This firewall has no configuration yet.
      <RouterLink to="/wizard" class="font-medium underline">Run the setup wizard</RouterLink>.
    </p>

    <DashboardWarnings :warnings="overview?.warnings ?? []" />

    <InterfaceSummary :interfaces="overview?.interfaces ?? []" :rates="rates" />

    <div class="grid gap-4 lg:grid-cols-2">
      <SystemLoadCard :stats="stats" />
      <GatewaysCard
        v-if="overview?.gateways?.length || overview?.unwatchedGateways?.length"
        :gateways="overview.gateways ?? []"
        :unwatched="overview.unwatchedGateways ?? []"
      />
      <TopRulesCard :rules="overview?.topRules ?? []" :blocked="overview?.blocked" />
      <RecentBlocksCard v-if="overview?.recentBlocks" :blocks="overview.recentBlocks" />
      <ServicesCard
        :services="overview?.services ?? []"
        :dhcp="overview?.dhcp ?? {}"
        :dns="overview?.dns ?? {}"
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
      />
    </div>
  </div>
</template>
