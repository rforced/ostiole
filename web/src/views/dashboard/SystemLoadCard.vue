<script setup>
import { TriangleAlert } from 'lucide-vue-next'
import { computed } from 'vue'

import SectionCard from '@/components/SectionCard.vue'
import { formatBytes, formatCount, formatDuration } from '@/lib/format'
import { LEVELS, levelOf } from '@/lib/meter'

const props = defineProps({
  /** A reading from GET /system/stats, or null before the first one. */
  stats: { type: Object, default: null },
})

function plural(n, word) {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/**
 * What the CPU meter is measuring. A chip with SMT runs two threads on
 * each core, and both numbers are worth knowing: the percentage is across
 * the threads, the hardware is the cores. Without SMT the two are the same
 * number and saying it twice is noise.
 */
function cpuDetail(cores, threads) {
  if (cores && threads && cores !== threads) return `${cores} cores, ${threads} threads`
  if (cores) return plural(cores, 'core')
  if (threads) return plural(threads, 'thread')
  return '? cores'
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
      : meter('CPU', s.cpuPercent, cpuDetail(s.cores, s.threads))

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
  <SectionCard title="System">
    <div class="space-y-3">
      <div v-for="m in meters" :key="m.label" :data-meter="m.label">
        <div class="flex items-baseline justify-between gap-3">
          <div class="flex items-center gap-1.5 text-ink-muted">
            <span>{{ m.label }}</span>
            <template v-if="m.level !== 'ok'">
              <TriangleAlert
                class="size-3.5"
                :class="m.level === 'critical' ? 'text-bad' : 'text-warn'"
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
            <span class="ml-2 text-ink-muted">{{ m.detail }}</span>
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
        <span class="ml-1 text-xs text-ink-muted">1 · 5 · 15 min</span>
      </dd>
      <dd v-else class="text-ink-muted">—</dd>

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
  </SectionCard>
</template>
