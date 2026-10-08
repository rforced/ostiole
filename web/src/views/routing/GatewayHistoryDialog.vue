<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import SeriesChart from '@/components/SeriesChart.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { niceScale } from '@/lib/chart'
import { formatDuration, formatMs, formatWhen } from '@/lib/format'
import {
  STEPS,
  addProbe,
  eventFor,
  eventText,
  historyRows,
  historySeries,
  measured,
  probeFigures,
} from '@/lib/gateways'
import { streamLost } from '@/lib/stream'
import { WINDOWS } from '@/lib/traffic'

/**
 * One gateway's latency and loss over a window, a line and a band and a
 * bar for each family it is probed in, with the window's figures and
 * events. Over five minutes it takes each probe from the stream.
 */
const props = defineProps({
  /** A row of the Gateways card: the configured gateway and what the monitor says. */
  gateway: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const REREAD_MS = 60_000
const win = ref('24h')
const report = ref(null)
const rows = ref(null)
const families = ref([])
const end = ref(0)

const read = useAsync(async () => {
  const name = props.gateway.name
  const w = win.value
  const r = await api.gatewayHistory.read(name, w)
  if (name !== props.gateway?.name || w !== win.value) return
  const h = historyRows(r)
  report.value = r
  rows.value = h.rows
  families.value = h.families
  end.value = Date.parse(r.now) / 1000
})

watch(
  () => [open.value, props.gateway?.name, win.value],
  ([on, name], before) => {
    if (!on || !name) return
    if (before?.[1] !== name) {
      report.value = null
      rows.value = null
    }
    read.run()
  },
  { immediate: true },
)

let timer = 0
let source = null
const streamError = ref('')
function listen() {
  stop()
  if (!open.value || win.value !== '5m' || document.visibilityState === 'hidden') return
  const es = new EventSource('/api/v1/gateways/stream')
  source = es
  es.onopen = () => (streamError.value = '')
  es.onmessage = (m) => {
    let p
    try {
      p = JSON.parse(m.data)
    } catch {
      return
    }
    if (p.gateway !== props.gateway?.name || !rows.value) return
    if (!families.value.includes(p.family)) {
      read.run()
      return
    }
    rows.value = addProbe(rows.value, families.value, p, '5m')
    end.value = Math.round(Date.parse(p.time) / 1000)
  }
  es.onerror = () => (streamError.value = streamLost(es))
}
function stop() {
  source?.close()
  source = null
}
watch(() => [open.value, win.value], listen, { immediate: true })
watch(open, (on) => {
  clearInterval(timer)
  if (on) {
    timer = setInterval(() => {
      if (win.value !== '5m' && document.visibilityState !== 'hidden') read.run()
    }, REREAD_MS)
  }
})
onMounted(() => document.addEventListener('visibilitychange', listen))
onBeforeUnmount(() => {
  stop()
  clearInterval(timer)
  document.removeEventListener('visibilitychange', listen)
})

const chart = computed(() => historySeries(families.value))
const scale = (highest) => niceScale(highest, 10)
const figures = computed(() => {
  const r = report.value
  if (!r) return []
  return families.value.map((family, k) => {
    if (win.value === '5m' && rows.value) {
      const f = probeFigures(rows.value, k, families.value.length)
      return { family, mean: f.mean, worst: f.worst, loss: f.loss, sent: f.sent }
    }
    const f = r.families.find((x) => x.family === family)
    return { family, mean: f.mean, worst: f.worst, loss: f.loss, sent: f.sent }
  })
})
const windowLabel = computed(() => WINDOWS.find((w) => w.value === win.value)?.label ?? '')
const error = computed(() => read.error.value || streamError.value)
</script>

<template>
  <AppDialog v-model:open="open" :title="gateway?.name ?? ''" :read-only="false">
    <div v-if="gateway" class="space-y-4">
      <div class="flex flex-wrap items-center gap-3">
        <select v-model="win" class="input w-36 max-sm:w-full" aria-label="Window">
          <option v-for="w in WINDOWS" :key="w.value" :value="w.value">{{ w.label }}</option>
        </select>
        <p class="text-sm text-ink-muted">
          Measured
          {{ measured(gateway.monitor, families.length || gateway.live?.families?.length) }}.
        </p>
      </div>
      <SeriesChart
        :points="rows"
        :window="win"
        :end="end"
        :label="gateway.name"
        :series="chart.series"
        :bars="chart.bars"
        :format="formatMs"
        :scale="scale"
        :gap="STEPS[win] * 2.5"
        :step="STEPS[win]"
        empty="No probes in this window."
        :stale="read.busy.value && rows !== null"
      />
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
      <dl v-if="report" class="kv text-sm">
        <template v-for="f in figures" :key="f.family">
          <dt>{{ f.family }}</dt>
          <dd class="tabular-nums">
            <template v-if="f.sent">
              {{ formatMs(f.mean) }} mean, {{ formatMs(f.worst) }} worst, {{ Math.round(f.loss) }}%
              lost
            </template>
            <template v-else>No probes</template>
          </dd>
        </template>
        <dt>Down in {{ windowLabel }}</dt>
        <dd class="tabular-nums">{{ report.down ? formatDuration(report.down) : 'None' }}</dd>
        <template v-if="report.never">
          <dt>Never answered</dt>
          <dd class="tabular-nums">{{ formatDuration(report.never) }}</dd>
        </template>
      </dl>
      <div v-if="report?.events?.length" class="space-y-2">
        <h3 class="group-title">Events in {{ windowLabel }}</h3>
        <table class="table">
          <thead>
            <tr>
              <th>Time</th>
              <th>What</th>
              <th class="num">For</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in report.events" :key="e.seq">
              <td class="when">
                {{ formatWhen(e.time) }}
              </td>
              <td>
                {{ eventText(e) }}
                <div v-if="e.error" class="text-xs text-ink-muted">{{ e.error }}</div>
              </td>
              <td class="num">{{ eventFor(e) || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </AppDialog>
</template>
