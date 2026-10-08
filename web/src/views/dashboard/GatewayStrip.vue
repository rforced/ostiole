<script setup>
import { computed } from 'vue'

import { formatDuration, formatMs } from '@/lib/format'
import { measured } from '@/lib/gateways'

/**
 * A gateway's last day as 144 cells of ten minutes, each its worst minute:
 * height for how bad, colour for what kind. Down is always the tallest, so
 * it reads without the colour; each cell's title gives its figures, and
 * the line under the strip the day's.
 */
const props = defineProps({
  /** One gateway of GET /gateways/strips. */
  strip: { type: Object, default: null },
  /** The gateway's name, for what the strip is said to be. */
  label: { type: String, required: true },
  /** Nothing is read yet: a grey strip and a grey line. */
  skeleton: { type: Boolean, default: false },
})

const HEIGHT = 24
const FILLS = {
  up: 'var(--ok-fill)',
  slow: 'var(--warn-fill)',
  lossy: 'var(--warn-fill)',
  down: 'var(--bad-fill)',
}
const WHAT = {
  up: 'up',
  slow: 'slow',
  lossy: 'losing packets',
  down: 'down',
  never: 'never answered',
}

/** How tall a cell is, as a share of the strip: no bar where nothing answered. */
function share(c) {
  if (c.kind === 'down') return 1
  if (c.kind === 'slow' || c.kind === 'lossy') return 0.5 + 0.25 * (c.level ?? 0)
  if (c.kind === 'up') return 0.25
  return 0
}

const cells = computed(() =>
  (props.strip?.cells ?? []).map((c, i) => ({
    ...c,
    i,
    h: share(c) * HEIGHT,
    fill: FILLS[c.kind],
  })),
)

function title(c) {
  const when = new Date(c.start * 1000).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
  })
  const parts = [`${when}: ${WHAT[c.kind] ?? 'no probes'}`]
  if (c.down) parts.push(`down ${formatDuration(c.down * 60)}`)
  if (c.latencyMs) parts.push(`worst ${formatMs(c.latencyMs)}`)
  if (c.lossPercent) parts.push(`${Math.round(c.lossPercent)}% lost`)
  return parts.join(', ')
}

const line = computed(() => {
  const s = props.strip
  if (!s) return ''
  if (!s.cells.some((c) => c.kind)) return 'No probes in 24 hours.'
  const of = (family) => (s.families > 1 && family ? ` (${family})` : '')
  const parts = [s.down ? `Down ${formatDuration(s.down)}` : 'Not down']
  if (s.worstLatencyMs) parts.push(`worst ${formatMs(s.worstLatencyMs)}${of(s.worstLatencyFamily)}`)
  parts.push(`worst ${Math.round(s.worstLossPercent)}% lost${of(s.worstLossFamily)}`)
  parts.push(measured(s.monitor, s.families))
  return parts.join(' · ')
})
</script>

<template>
  <div class="space-y-1">
    <svg
      :viewBox="`0 0 144 ${HEIGHT}`"
      preserveAspectRatio="none"
      class="block h-6 w-full"
      role="img"
      :aria-label="
        skeleton ? `${label}, the last 24 hours: reading` : `${label}, the last 24 hours: ${line}`
      "
    >
      <rect
        v-if="skeleton"
        width="144"
        :height="HEIGHT"
        class="animate-pulse fill-surface-2 motion-reduce:animate-none"
      />
      <template v-for="c in cells" v-else :key="c.start">
        <rect :x="c.i + 0.1" y="0" width="0.8" :height="HEIGHT" fill="var(--line)">
          <title>{{ title(c) }}</title>
        </rect>
        <rect
          v-if="c.h"
          :x="c.i + 0.1"
          :y="HEIGHT - c.h"
          width="0.8"
          :height="c.h"
          :fill="c.fill"
          :data-kind="c.kind"
        >
          <title>{{ title(c) }}</title>
        </rect>
      </template>
    </svg>
    <p class="text-xs text-ink-muted">
      <span v-if="skeleton" class="skeleton w-56"></span>
      <template v-else>{{ line }}</template>
    </p>
  </div>
</template>
