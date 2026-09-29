<script setup>
import { computed, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatCount, formatRate } from '@/lib/format'
import { LEVELS } from '@/lib/meter'
import { TIERS, tierBadge, tierLabel } from '@/views/firewall/shaping/tiers'

/** How often the queues are sampled. Fast enough to watch a call start. */
const INTERVAL_MS = 3000

const report = ref(null)
const rates = ref({})
/** The previous sample, so a rate can be a difference rather than a total. */
let previous = null

/**
 * The counters the router reports are cumulative, which is what makes them
 * safe to read from more than one place at once. A rate is the difference
 * between two samples, worked out here rather than kept as history on a
 * router nobody asked to keep any.
 */
function counters(rep) {
  const out = {}
  for (const i of rep?.interfaces ?? []) {
    for (const dir of ['download', 'upload']) {
      const stats = i[dir]?.stats
      if (!stats) continue
      out[`${i.name}:${dir}`] = stats.bytes
      for (const tin of stats.tins ?? []) {
        if (tin.tier) out[`${i.name}:${dir}:${tin.tier}`] = tin.sentBytes
      }
    }
  }
  return out
}

function ratesBetween(before, after) {
  const out = {}
  if (!before) return out
  const seconds = (Date.parse(after.sampledAt) - Date.parse(before.sampledAt)) / 1000
  if (!(seconds > 0)) return out
  const was = counters(before)
  const now = counters(after)
  for (const key of Object.keys(now)) {
    const had = was[key]
    // A queue that has just been reinstalled counts from zero again; a
    // difference that went backwards is that, not a negative rate.
    if (had === undefined || now[key] < had) continue
    out[key] = ((now[key] - had) * 8) / seconds
  }
  return out
}

const live = useAsync(
  async () => {
    const next = await api.shaping()
    rates.value = ratesBetween(previous, next)
    previous = next
    report.value = next
  },
  { interval: INTERVAL_MS, immediate: true },
)

const interfaces = computed(() => report.value?.interfaces ?? [])

/** The directions of one interface that were given a figure at all. */
function directions(iface) {
  return [
    { name: 'download', label: 'Download', state: iface.download },
    { name: 'upload', label: 'Upload', state: iface.upload },
  ].filter((d) => d.state?.rate)
}

function rateOf(key) {
  return rates.value[key]
}

/** How full the line is, as far as the meter is concerned. */
function fill(key, ceiling) {
  const bits = rateOf(key)
  if (!bits || !ceiling) return 0
  return Math.max(0, Math.min(100, (bits / ceiling) * 100))
}

/** The tier rows of one direction, in the order the router names them. */
function tins(state) {
  const byTier = new Map((state?.stats?.tins ?? []).map((t) => [t.tier, t]))
  return TIERS.map((t) => ({ tier: t.value, ...(byTier.get(t.value) ?? {}) }))
}

/** Microseconds as the milliseconds an operator thinks in. */
function delay(us) {
  if (!us) return '0 ms'
  const ms = us / 1000
  return `${ms >= 10 ? ms.toFixed(0) : ms.toFixed(1)} ms`
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Queues">
      <template #actions>
        <RefreshButton
          :busy="live.busy.value"
          :updated-at="live.updatedAt.value"
          @click="live.run"
        />
      </template>
      <div class="space-y-3">
        <AppNotice v-if="live.error.value" kind="bad">{{ live.error.value }}</AppNotice>
        <AppNotice v-else-if="report && !report.available" data-testid="shaping-unavailable">
          {{ report.reason }}
        </AppNotice>
        <p v-if="report && !interfaces.length" class="text-ink-muted">
          No interface has a speed, so there is nothing to watch.
        </p>
        <p v-else class="max-w-3xl text-ink-muted">
          Peak delay is how long the longest-waiting packet sat in the queue. Staying low while the
          line is full is what shaping is for.
        </p>
      </div>
    </SectionCard>

    <SectionCard v-for="iface in interfaces" :key="iface.name">
      <template #title>
        <span class="font-mono">{{ iface.name }}</span>
        <span v-if="iface.zone" class="badge">{{ iface.zone }}</span>
        <span v-if="iface.description" class="font-normal text-ink-muted">
          {{ iface.description }}
        </span>
      </template>
      <AppNotice v-if="!iface.present" class="mb-4">
        This link is not up yet, so nothing is queued on it.
      </AppNotice>

      <div class="grid gap-6 sm:grid-cols-2">
        <div v-for="d in directions(iface)" :key="d.name" class="space-y-2">
          <div class="text-ink-muted">{{ d.label }}</div>
          <div class="text-2xl leading-tight tabular-nums">
            {{
              rateOf(`${iface.name}:${d.name}`) === undefined
                ? '—'
                : formatRate(rateOf(`${iface.name}:${d.name}`))
            }}
            <span class="text-base text-ink-muted">of {{ formatRate(d.state.rate) }}</span>
          </div>
          <div
            class="meter"
            :class="LEVELS.ok.track"
            role="meter"
            :aria-label="`${iface.name} ${d.label}`"
            :aria-valuenow="Math.round(fill(`${iface.name}:${d.name}`, d.state.rate))"
            aria-valuemin="0"
            aria-valuemax="100"
          >
            <div
              class="meter-fill"
              :class="LEVELS.ok.fill"
              :style="{ width: `${fill(`${iface.name}:${d.name}`, d.state.rate)}%` }"
            ></div>
          </div>

          <p v-if="!d.state.installed" class="text-ink-muted">Not in the kernel yet.</p>
          <div v-else class="overflow-x-auto max-lg:relative max-lg:scroll-fade">
            <table class="table">
              <thead>
                <tr>
                  <th>Priority</th>
                  <th class="text-right">Rate</th>
                  <th class="text-right">Peak delay</th>
                  <th class="text-right">Drops</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="t in tins(d.state)" :key="t.tier">
                  <td>
                    <span :class="tierBadge(t.tier)">{{ tierLabel(t.tier) }}</span>
                  </td>
                  <td class="text-right font-mono text-code tabular-nums">
                    {{
                      rateOf(`${iface.name}:${d.name}:${t.tier}`) === undefined
                        ? '—'
                        : formatRate(rateOf(`${iface.name}:${d.name}:${t.tier}`))
                    }}
                  </td>
                  <td class="text-right font-mono text-code tabular-nums">
                    {{ delay(t.peakDelayUs) }}
                  </td>
                  <td class="text-right font-mono text-code tabular-nums">
                    {{ formatCount(t.drops ?? 0) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </SectionCard>
  </div>
</template>
