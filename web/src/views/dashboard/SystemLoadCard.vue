<script setup>
import { TriangleAlert } from 'lucide-vue-next'
import { computed } from 'vue'

import { formatBytes, formatCount, formatDuration } from '@/lib/format'

const props = defineProps({
  /** A reading from GET /system/stats, or null before the first one. */
  stats: { type: Object, default: null },
})

/**
 * Where a meter stops being reassuring. A router at 80% memory is worth a
 * glance; at 90% of a disk it is worth acting on, and a connection table
 * at 100% refuses new connections outright.
 */
const WARN = 75
const CRITICAL = 90

/**
 * Severity colours the fill, and the track takes a washed-out step of the
 * same hue so the state reads across the whole bar. Dark mode gets its own
 * track rather than a flipped one: the 950 step of a ramp is still a
 * saturated colour against a near-black surface, and at this height it
 * reads as a full bar.
 *
 * The percentage beside the meter is always visible and stays in ordinary
 * ink: the colour repeats what the number already says rather than being
 * the only way to read it, which is what keeps the card legible to a
 * colour-blind reader and in print.
 */
const LEVELS = {
  ok: {
    fill: 'bg-sky-600 dark:bg-sky-500',
    track: 'bg-sky-100 dark:bg-sky-500/20',
  },
  warn: {
    fill: 'bg-amber-500 dark:bg-amber-400',
    track: 'bg-amber-100 dark:bg-amber-400/20',
  },
  critical: {
    fill: 'bg-red-600 dark:bg-red-500',
    track: 'bg-red-100 dark:bg-red-500/20',
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

/** The filesystem the meter shows: the root one, or whatever came first. */
const rootDisk = computed(() => {
  const all = (props.stats?.filesystems ?? []).filter((fs) => fs.total)
  return all.find((fs) => fs.path === '/') ?? all[0] ?? null
})

/**
 * A second filesystem, when the configuration lives on one of its own. It
 * goes in the list below rather than among the meters, which are the
 * things that fill up on their own.
 */
const otherDisk = computed(
  () =>
    (props.stats?.filesystems ?? []).filter((fs) => fs.total && fs !== rootDisk.value)[0] ?? null,
)

/**
 * The three meters every router has, then the connection table once the
 * kernel reports one. A router that has never filtered has no table, and
 * a meter of zero over zero would say less than no meter.
 */
const meters = computed(() => {
  const s = props.stats
  if (!s) return [blank('CPU'), blank('Memory'), blank('Disk')]

  const cpu =
    s.cpuPercent === null || s.cpuPercent === undefined
      ? blank('CPU')
      : meter('CPU', s.cpuPercent, `${s.cores || '?'} core${s.cores === 1 ? '' : 's'}`)

  let memory = blank('Memory')
  if (s.memTotal) {
    const used = s.memTotal - s.memAvailable
    memory = meter(
      'Memory',
      (used / s.memTotal) * 100,
      `${formatBytes(used)} of ${formatBytes(s.memTotal)}`,
    )
  }

  let disk = blank('Disk')
  const fs = rootDisk.value
  if (fs) {
    const used = fs.total - fs.free
    disk = meter(
      'Disk',
      (used / fs.total) * 100,
      `${formatBytes(used)} of ${formatBytes(fs.total)}`,
    )
  }

  const out = [cpu, memory, disk]
  const ct = s.conntrack
  if (ct?.max) {
    out.push(
      meter(
        'States',
        (ct.count / ct.max) * 100,
        `${formatCount(ct.count)} of ${formatCount(ct.max)}`,
      ),
    )
  }
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

    <div class="space-y-3">
      <div v-for="m in meters" :key="m.label" :data-meter="m.label">
        <div class="flex items-baseline justify-between gap-3">
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
          <div class="text-right">
            <span class="font-medium tabular-nums">
              <template v-if="m.percent === null">—</template>
              <template v-else>{{ m.percent.toFixed(0) }}%</template>
            </span>
            <span class="ml-2 text-neutral-500">{{ m.detail }}</span>
          </div>
        </div>
        <div
          class="meter mt-1"
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

      <template v-if="otherDisk">
        <dt class="font-mono">{{ otherDisk.path }}</dt>
        <dd>
          {{ formatBytes(otherDisk.total - otherDisk.free) }} of
          {{ formatBytes(otherDisk.total) }} used
        </dd>
      </template>
    </dl>
  </section>
</template>
