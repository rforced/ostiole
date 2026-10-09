<script setup>
import { computed } from 'vue'

import SeriesChart from '@/components/SeriesChart.vue'
import { formatRate } from '@/lib/format'

/**
 * What a link or a device moved over a window: down and up as two lines
 * on one axis, in bits per second.
 */
const props = defineProps({
  /** [seconds, down, up], oldest first; null until the first read. */
  points: { type: Array, default: null },
  /** A key of SPANS: 5m, 24h or 30d. */
  window: { type: String, default: '5m' },
  /** The right edge in seconds; the last point's time when unset. */
  end: { type: Number, default: 0 },
  /** What is charted, as the page names it: eth0, or a device. */
  label: { type: String, required: true },
  /** Down and up now, for the legend; the last point's when unset. */
  now: { type: Object, default: null },
  /** A new window is being read, and this is still the old one. */
  stale: { type: Boolean, default: false },
  /** Nothing is read yet, not even what is charted. */
  skeleton: { type: Boolean, default: false },
})

const SERIES = [
  { index: 1, name: 'Down', color: 'var(--chart-down)' },
  { index: 2, name: 'Up', color: 'var(--chart-up)' },
]
const now = computed(() => (props.now ? [props.now.down, props.now.up] : null))
</script>

<template>
  <SeriesChart
    :points="points"
    :window="window"
    :end="end"
    :label="label"
    :series="SERIES"
    :format="formatRate"
    :now="now"
    :stale="stale"
    :skeleton="skeleton"
  />
</template>
