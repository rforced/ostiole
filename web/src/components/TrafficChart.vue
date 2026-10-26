<script setup lang="ts">
import type { PropType } from 'vue'
import { computed } from 'vue'

import SeriesChart from '@/components/SeriesChart.vue'
import { formatRate } from '@/lib/format'

import type { ChartPoint } from './chartTypes'

/**
 * What a link or a device moved over a window: down and up as two lines
 * on one axis, in bits per second.
 */
const props = defineProps({
  /** [seconds, down, up], oldest first; null until the first read. */
  points: { type: Array as PropType<ChartPoint[] | null>, default: null },
  /** A key of SPANS: 5m, 24h or 30d. */
  window: { type: String, default: '5m' },
  /** The right edge in seconds; the last point's time when unset. */
  end: { type: Number, default: 0 },
  /** What is charted, as the page names it: eth0, or a device. */
  label: { type: String, required: true },
  /** Down and up now, for the legend; the last point's when unset. */
  now: { type: Object as PropType<{ down: number; up: number } | null>, default: null },
  /** A new window is being read, and this is still the old one. */
  stale: { type: Boolean, default: false },
  /** Nothing is read yet, not even what is charted. */
  skeleton: { type: Boolean, default: false },
  /** The speed the link is shaped to, {down, up} in bits per second, 0 where unshaped. */
  speed: { type: Object as PropType<{ down: number; up: number } | null>, default: null },
})

const series = computed(() => [
  { index: 1, name: 'Down', color: 'var(--chart-down)', limit: props.speed?.down ?? 0 },
  { index: 2, name: 'Up', color: 'var(--chart-up)', limit: props.speed?.up ?? 0 },
])
const now = computed(() => (props.now ? [props.now.down, props.now.up] : null))
</script>

<template>
  <SeriesChart
    :points="points"
    :window="window"
    :end="end"
    :label="label"
    :series="series"
    :format="formatRate"
    :now="now"
    :stale="stale"
    :skeleton="skeleton"
  />
</template>
