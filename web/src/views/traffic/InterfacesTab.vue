<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import TrafficChart from '@/components/TrafficChart.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes } from '@/lib/format'
import { WINDOWS, appendPoint, sinceLine, useTrafficStream } from '@/lib/traffic'
import { useConfigStore } from '@/stores/config'

/** How often a day's or a month's charts are read again while Live is on. */
const REREAD_MS = 60_000

const win = ref('5m')
const live = ref(true)
/** The links over the window, null before the first read. */
const links = ref(null)
/** The router's clock at the window's end, in seconds. */
const end = ref(0)
/** Rates that arrived while Live was off, oldest first. */
let held = []

const read = useAsync(async () => {
  const w = win.value
  const r = await api.traffic.interfaces(w)
  if (w !== win.value) return
  links.value = r.links
  end.value = Date.parse(r.now) / 1000
  held = []
})
onMounted(read.run)
watch(win, () => read.run())

/** A second of every link: its rate now, and over five minutes a point. */
function take(ev) {
  const t = Date.parse(ev.time) / 1000
  for (const r of ev.links) {
    const l = links.value?.find((x) => x.name === r.id)
    if (!l) continue
    l.down = r.down
    l.up = r.up
    if (win.value === '5m') l.points = appendPoint(l.points ?? [], [t, r.down, r.up], '5m')
  }
  if (win.value === '5m') end.value = t
}

const stream = useTrafficStream({
  links: (ev) => {
    if (live.value) take(ev)
    else held = [...held, ev].slice(-300)
  },
  reopened: () => read.run(),
})

// Off, the charts hold and the seconds wait; on again, they are drawn.
watch(live, (on) => {
  if (!on) return
  const waiting = held
  held = []
  for (const ev of waiting) take(ev)
})

// A day takes a new point each minute and a month each hour; reading
// again each minute draws both.
let timer = 0
onMounted(() => {
  timer = setInterval(() => {
    if (live.value && win.value !== '5m' && document.visibilityState !== 'hidden') read.run()
  }, REREAD_MS)
})
onBeforeUnmount(() => clearInterval(timer))

/** Empties what every link moved, its errors and files included, and reads again. */
const clear = useAsync(async () => {
  await api.traffic.clearInterfaces()
  await read.run()
})
const config = useConfigStore()
/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

const stale = computed(() => read.busy.value && links.value !== null)
const error = computed(() => read.error.value || stream.error.value || clear.error.value)
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Interfaces" :count="links?.length" flush>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(error)" />
        <ClearLogButton
          name="traffic per interface"
          :description="
            inFiles
              ? 'What every interface moved and its errors are dropped, files included. Counting carries on.'
              : 'What every interface moved and its errors are dropped. Counting carries on.'
          "
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip-row">
        <select v-model="win" class="input w-48 max-sm:w-full" aria-label="Window">
          <option v-for="w in WINDOWS" :key="w.value" :value="w.value">{{ w.label }}</option>
        </select>
        <ErrorLine v-if="error">{{ error }}</ErrorLine>
      </div>
      <p v-if="!links" class="card-strip border-t border-line text-ink-muted">Reading…</p>
      <p v-else-if="!links.length" class="card-strip border-t border-line text-ink-muted">
        No interfaces.
      </p>
      <section
        v-for="l in links ?? []"
        :key="l.name"
        class="border-t border-line px-4 pt-4 pb-4"
        :aria-labelledby="`link-${l.name}`"
      >
        <h3 class="card-title flex items-center gap-2">
          <span :id="`link-${l.name}`" class="font-mono">{{ l.name }}</span>
          <span v-if="l.external" class="badge">WAN</span>
          <span v-if="!l.configured" class="text-xs font-normal text-ink-muted">unmanaged</span>
        </h3>
        <p v-if="l.description" class="mt-0.5 max-w-3xl text-ink-muted">{{ l.description }}</p>
        <div class="mt-3 grid gap-4 lg:grid-cols-[minmax(0,1fr)_11rem]">
          <TrafficChart
            :points="l.points ?? []"
            :window="win"
            :end="end"
            :label="l.name"
            :now="{ down: l.down, up: l.up }"
            :stale="stale"
          />
          <div class="space-y-2">
            <dl class="kv">
              <dt>Down</dt>
              <dd class="tabular-nums">{{ formatBytes(l.totals?.down ?? 0) }}</dd>
              <dt>Up</dt>
              <dd class="tabular-nums">{{ formatBytes(l.totals?.up ?? 0) }}</dd>
            </dl>
            <p v-if="sinceLine(l.since, win, end)" class="text-xs text-ink-muted">
              {{ sinceLine(l.since, win, end) }}
            </p>
          </div>
        </div>
      </section>
    </SectionCard>
  </div>
</template>
