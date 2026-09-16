<script setup>
import { TriangleAlert } from 'lucide-vue-next'
import { computed } from 'vue'

import { formatBytes, formatDuration } from '@/lib/format'

const props = defineProps({
  /** A reading from GET /system/stats, or null before the first one. */
  stats: { type: Object, default: null },
})

/**
 * Where a meter stops being reassuring. A router at 80% memory is worth a
 * glance; at 90% of a disk it is worth acting on.
 */
const WARN = 75
const CRITICAL = 90

/**
 * Severity colours the fill, and the track takes a light step of the same
 * hue so the state reads across the whole bar. The percentage beside it is
 * always visible and stays in ordinary ink: the colour repeats what the
 * number already says rather than being the only way to read it, which is
 * what makes the card safe for a colour-blind reader and in print.
 */
const LEVELS = {
  ok: {
    fill: 'bg-sky-600 dark:bg-sky-500',
    track: 'bg-sky-100 dark:bg-sky-950',
  },
  warn: {
    fill: 'bg-amber-500 dark:bg-amber-400',
    track: 'bg-amber-100 dark:bg-amber-950',
  },
  critical: {
    fill: 'bg-red-600 dark:bg-red-500',
    track: 'bg-red-100 dark:bg-red-950',
  },
}

function levelOf(percent) {
  if (percent === null) return 'ok'
  if (percent >= CRITICAL) return 'critical'
  if (percent >= WARN) return 'warn'
  return 'ok'
}

/** A meter with nothing to show yet, so the card keeps its shape. */
function blank(label) {
  return { label, percent: null, detail: 'no reading yet', level: 'ok' }
}

function meter(label, percent, detail) {
  const pct = Math.max(0, Math.min(100, percent))
  return { label, percent: pct, detail, level: levelOf(pct) }
}

const meters = computed(() => {
  const s = props.stats
  if (!s) return [blank('CPU'), blank('Memory'), blank('Disk')]
  const out = []

  out.push(
    s.cpuPercent === null || s.cpuPercent === undefined
      ? blank('CPU')
      : meter('CPU', s.cpuPercent, `${s.cores || '?'} core${s.cores === 1 ? '' : 's'}`),
  )

  if (s.memTotal) {
    const used = s.memTotal - s.memAvailable
    out.push(
      meter(
        'Memory',
        (used / s.memTotal) * 100,
        `${formatBytes(used)} of ${formatBytes(s.memTotal)}`,
      ),
    )
  } else {
    out.push(blank('Memory'))
  }

  // The root filesystem is the one that matters; a box that keeps /etc
  // apart gets that meter too.
  for (const fs of s.filesystems ?? []) {
    if (!fs.total) continue
    const used = fs.total - fs.free
    out.push(
      meter(
        fs.path === '/' ? 'Disk' : `Disk ${fs.path}`,
        (used / fs.total) * 100,
        `${formatBytes(used)} of ${formatBytes(fs.total)}`,
      ),
    )
  }
  if (out.length === 2) out.push(blank('Disk'))
  return out
})

const swap = computed(() => {
  const s = props.stats
  if (!s?.swapTotal) return ''
  return `${formatBytes(s.swapTotal - s.swapFree)} of ${formatBytes(s.swapTotal)} used`
})
</script>

<template>
  <section class="card" aria-labelledby="dash-load">
    <h2 id="dash-load" class="card-title">System</h2>

    <div class="grid gap-4 sm:grid-cols-3">
      <div v-for="m in meters" :key="m.label" class="space-y-1">
        <div class="flex items-center gap-1.5 text-neutral-500">
          <span>{{ m.label }}</span>
          <template v-if="m.level !== 'ok'">
            <TriangleAlert
              class="size-3.5"
              :class="
                m.level === 'critical'
                  ? 'text-red-600 dark:text-red-500'
                  : 'text-amber-600 dark:text-amber-400'
              "
              aria-hidden="true"
            />
            <span class="sr-only">{{ m.level === 'critical' ? 'critical' : 'high' }}</span>
          </template>
        </div>
        <div class="text-2xl leading-tight">
          <template v-if="m.percent === null">—</template>
          <template v-else
            >{{ m.percent.toFixed(0) }}<span class="text-base text-neutral-500">%</span></template
          >
        </div>
        <div
          class="meter"
          :class="LEVELS[m.level].track"
          role="meter"
          :aria-label="`${m.label} usage`"
          :aria-valuenow="m.percent === null ? 0 : Math.round(m.percent)"
          aria-valuemin="0"
          aria-valuemax="100"
        >
          <div
            class="meter-fill"
            :class="LEVELS[m.level].fill"
            :style="{ width: `${m.percent ?? 0}%` }"
          ></div>
        </div>
        <div class="text-xs text-neutral-500">{{ m.detail }}</div>
      </div>
    </div>

    <dl class="kv mt-4">
      <dt>Load</dt>
      <dd v-if="stats" class="font-mono tabular-nums">
        {{ stats.load1.toFixed(2) }} · {{ stats.load5.toFixed(2) }} · {{ stats.load15.toFixed(2) }}
        <span class="ml-1 text-xs text-neutral-500">1 · 5 · 15 min</span>
      </dd>
      <dd v-else class="text-neutral-500">—</dd>

      <dt>Uptime</dt>
      <dd>{{ stats ? formatDuration(stats.uptimeSeconds) : '—' }}</dd>

      <template v-if="swap">
        <dt>Swap</dt>
        <dd>{{ swap }}</dd>
      </template>
    </dl>
  </section>
</template>
