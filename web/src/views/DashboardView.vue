<script setup>
import { RefreshCw } from 'lucide-vue-next'
import { computed, onMounted, onUnmounted, ref } from 'vue'

import ApplyPending from '@/components/ApplyPending.vue'
import { api } from '@/lib/api'
import { useSystemStore } from '@/stores/system'
import DashboardWarnings from '@/views/dashboard/DashboardWarnings.vue'
import GatewaysCard from '@/views/dashboard/GatewaysCard.vue'
import InterfaceSummary from '@/views/dashboard/InterfaceSummary.vue'
import ServicesCard from '@/views/dashboard/ServicesCard.vue'
import SystemLoadCard from '@/views/dashboard/SystemLoadCard.vue'
import TopRulesCard from '@/views/dashboard/TopRulesCard.vue'

/** Live enough for counters and carrier, quiet enough for a router. */
const REFRESH_MS = 10_000
/**
 * Usage is sampled faster, because a meter that only moves every ten
 * seconds does not read as live. It is two small file reads on the box.
 */
const STATS_MS = 3_000

const system = useSystemStore()
const health = ref(null)
const overview = ref(null)
const stats = ref(null)
const error = ref('')
const update = ref(null)
const status = computed(() => system.status)
let timer = null
let statsTimer = null

/** Usage failing is never worth an error banner over the whole dashboard. */
async function refreshStats() {
  try {
    stats.value = await api.systemStats()
  } catch {
    stats.value = null
  }
}

async function refresh() {
  try {
    const [ov] = await Promise.all([api.overview(), system.refresh()])
    overview.value = ov
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(async () => {
  try {
    health.value = await api.health()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
  await refresh()
  timer = setInterval(refresh, REFRESH_MS)
  await refreshStats()
  statsTimer = setInterval(refreshStats, STATS_MS)
  // Best effort: a quiet hint when a newer release exists.
  try {
    const res = await api.update.check(
      localStorage.getItem('ostiole.updateChannel') === 'beta' ? 'beta' : 'stable',
    )
    if (res.check?.available) update.value = res.check
  } catch {
    /* offline or updates unavailable */
  }
})

onUnmounted(() => {
  clearInterval(timer)
  clearInterval(statsTimer)
})
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between gap-4">
      <h1 class="text-2xl font-semibold tracking-tight">Dashboard</h1>
      <button type="button" class="btn-secondary" @click="refresh">
        <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Refresh
      </button>
    </div>
    <p v-if="error || system.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ error || system.error }}
    </p>

    <ApplyPending
      v-if="status?.pending"
      :key="status.pending.deadline"
      :deadline="status.pending.deadline"
      @confirmed="refresh()"
      @reverted="refresh()"
    />

    <p
      v-if="status && !status.configured"
      class="rounded-lg border border-sky-300 bg-sky-50 p-4 text-sm dark:border-sky-800 dark:bg-sky-950/40"
    >
      This firewall has no configuration yet.
      <RouterLink to="/wizard" class="font-medium underline">Run the setup wizard</RouterLink>.
    </p>

    <DashboardWarnings :warnings="overview?.warnings ?? []" />

    <SystemLoadCard :stats="stats" />

    <InterfaceSummary :interfaces="overview?.interfaces ?? []" />

    <GatewaysCard :gateways="overview?.gateways ?? []" />

    <div class="grid gap-4 lg:grid-cols-2">
      <TopRulesCard :rules="overview?.topRules ?? []" :blocked="overview?.blocked" />
      <ServicesCard
        :services="overview?.services ?? []"
        :dhcp="overview?.dhcp ?? {}"
        :dns="overview?.dns ?? {}"
      />
    </div>

    <div class="grid gap-4 sm:grid-cols-2">
      <section class="card" aria-labelledby="fw-status">
        <h2 id="fw-status" class="card-title">Firewall</h2>
        <dl v-if="status" class="kv">
          <dt>Configured</dt>
          <dd>{{ status.configured ? 'yes' : 'no' }}</dd>
          <dt>Ruleset loaded</dt>
          <dd>{{ status.tableLoaded ? 'yes' : 'no' }}</dd>
          <dt>Network backend</dt>
          <dd>{{ status.network }}</dd>
          <dt>Rules</dt>
          <dd>
            {{ overview?.status?.rules ?? 0 }} in {{ overview?.status?.zones ?? 0 }} zones ·
            <RouterLink to="/firewall/rules" class="link">edit</RouterLink>
          </dd>
          <dt>Pending apply</dt>
          <dd>
            {{
              status.pending
                ? `until ${new Date(status.pending.deadline).toLocaleTimeString()}`
                : 'none'
            }}
          </dd>
        </dl>
        <p v-else class="text-neutral-500">Loading…</p>
      </section>

      <section class="card" aria-labelledby="backend-status">
        <h2 id="backend-status" class="card-title">Backend</h2>
        <dl v-if="health" class="kv">
          <dt>Status</dt>
          <dd>{{ health.status }}</dd>
          <dt>Hostname</dt>
          <dd class="font-mono">{{ overview?.status?.hostname || '—' }}</dd>
          <dt>Version</dt>
          <dd class="font-mono">{{ health.version }}</dd>
          <dt>Commit</dt>
          <dd class="font-mono">{{ health.commit }}</dd>
          <dt>Revisions</dt>
          <dd>
            {{ overview?.status?.revisions ?? 0 }} ·
            <RouterLink to="/system/backup" class="link">roll back under System</RouterLink>
          </dd>
          <template v-if="update">
            <dt>Update</dt>
            <dd>
              <span class="font-mono">{{ update.latest }}</span> available ·
              <RouterLink to="/system/updates" class="link">install under System</RouterLink>
            </dd>
          </template>
        </dl>
        <p v-else class="text-neutral-500">Loading…</p>
      </section>
    </div>
  </div>
</template>
