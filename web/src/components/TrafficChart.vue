<script setup>
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue'

import {
  SPANS,
  linePath,
  nearest,
  niceScale,
  pointLabel,
  rateTicks,
  timeLabel,
  timeTicks,
} from '@/lib/chart'
import { formatRate } from '@/lib/format'

/**
 * What a link or a device moved over a window: down and up as two lines
 * on one axis, in bits per second. The legend keys each line and says
 * its rate now; a crosshair snaps to the nearest point under the pointer
 * or the arrow keys and reads both values there; Table shows the same
 * points as rows. Nothing moves: a new point redraws the frame, and a new
 * window keeps the old frame, dimmed, until its points arrive.
 */
const props = defineProps({
  /** [seconds, down, up], oldest first; null until the first read. */
  points: { type: Array, default: null },
  /** A key of SPANS: 5m, 24h or 31d. */
  window: { type: String, default: '5m' },
  /** The right edge in seconds; the last point's time when unset. */
  end: { type: Number, default: 0 },
  /** What is charted, as the page names it: eth0, or a device. */
  label: { type: String, required: true },
  /** Down and up now, for the legend; the last point's when unset. */
  now: { type: Object, default: null },
  /** A new window is being read, and this is still the old one. */
  stale: { type: Boolean, default: false },
  /**
   * Nothing is read yet, not even what is charted: grey where the rates,
   * the times and the traffic go, as the dashboard's other cards wait.
   */
  skeleton: { type: Boolean, default: false },
})

/** The plot and the time axis under it, in pixels. */
const HEIGHT = 168
const PAD = { top: 10, right: 12, bottom: 24, left: 76 }
/** The traffic a skeleton draws, as fractions of the plot's height. */
const WAVE = [0.3, 0.42, 0.36, 0.5, 0.44, 0.58, 0.4, 0.48, 0.34, 0.46, 0.38]

const id = useId()
const box = ref(null)
const svg = ref(null)
const width = ref(640)
let observer = null
onMounted(() => {
  if (typeof ResizeObserver === 'undefined' || !box.value) return
  observer = new ResizeObserver(([entry]) => {
    width.value = Math.max(240, Math.round(entry.contentRect.width))
  })
  observer.observe(box.value)
})
onBeforeUnmount(() => observer?.disconnect())

const span = computed(() => SPANS[props.window] ?? SPANS['5m'])
const all = computed(() => props.points ?? [])
const endT = computed(() => props.end || all.value.at(-1)?.[0] || Date.now() / 1000)
const startT = computed(() => endT.value - span.value)
/** The points inside the window, oldest first. */
const shown = computed(() => all.value.filter((p) => p[0] >= startT.value && p[0] <= endT.value))
const scale = computed(() => {
  let highest = 0
  for (const p of shown.value) highest = Math.max(highest, p[1], p[2])
  return niceScale(highest)
})

const plotW = computed(() => width.value - PAD.left - PAD.right)
const plotH = HEIGHT - PAD.top - PAD.bottom
const x = (t) => PAD.left + ((t - startT.value) / span.value) * plotW.value
const y = (v) => PAD.top + plotH - (v / scale.value.top) * plotH

const down = computed(() => linePath(shown.value, 1, x, y))
const up = computed(() => linePath(shown.value, 2, x, y))
const rates = computed(() => rateTicks(scale.value))
const times = computed(() => timeTicks(startT.value, endT.value, props.window, width.value < 480))
const last = computed(() => shown.value.at(-1) ?? null)
const current = computed(
  () => props.now ?? (last.value ? { down: last.value[1], up: last.value[2] } : null),
)

/** The skeleton's traffic: an area over the zero line, curved through WAVE. */
const wave = computed(() => {
  const base = PAD.top + plotH
  const step = plotW.value / (WAVE.length - 1)
  const p = WAVE.map((f, i) => [PAD.left + i * step, base - f * plotH])
  const at = ([px, py]) => `${px.toFixed(1)},${py.toFixed(1)}`
  let d = `M${at([PAD.left, base])}L${at(p[0])}`
  for (let i = 1; i < p.length - 1; i++) {
    d += `Q${at(p[i])} ${at([(p[i][0] + p[i + 1][0]) / 2, (p[i][1] + p[i + 1][1]) / 2])}`
  }
  return `${d}L${at(p.at(-1))}L${at([p.at(-1)[0], base])}Z`
})

const summary = computed(() => {
  if (!props.points) return `${props.label}: reading`
  if (!current.value) return `${props.label}: nothing counted yet`
  return `${props.label}: down ${formatRate(current.value.down)}, up ${formatRate(current.value.up)}`
})

/** The point the crosshair is on, as an index into shown, or -1. */
const at = ref(-1)
const point = computed(() => (at.value >= 0 ? (shown.value[at.value] ?? null) : null))

function pointer(e) {
  const rect = svg.value?.getBoundingClientRect()
  if (!rect?.width || !shown.value.length) return
  const px = ((e.clientX - rect.left) / rect.width) * width.value
  at.value = nearest(shown.value, startT.value + ((px - PAD.left) / plotW.value) * span.value)
}

function key(e) {
  const n = shown.value.length
  if (!n) return
  const from = at.value < 0 ? n : at.value
  const moves = {
    ArrowLeft: Math.max(0, from - 1),
    ArrowRight: Math.min(n - 1, at.value < 0 ? n - 1 : from + 1),
    Home: 0,
    End: n - 1,
    Escape: -1,
  }
  if (!(e.key in moves)) return
  e.preventDefault()
  at.value = moves[e.key]
}

/** The readout sits beside the crosshair, and inside the chart. */
const readoutStyle = computed(() => {
  if (!point.value) return undefined
  const px = x(point.value[0])
  return px > width.value / 2
    ? { right: `${Math.round(width.value - px + 8)}px` }
    : { left: `${Math.round(px + 8)}px` }
})

const table = ref(false)
</script>

<template>
  <div ref="box" class="min-w-0 space-y-2">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
      <span class="flex items-center gap-1.5">
        <svg width="16" height="4" aria-hidden="true">
          <line x1="0" y1="2" x2="16" y2="2" stroke="var(--chart-down)" stroke-width="2" />
        </svg>
        Down
        <span v-if="skeleton" class="skeleton w-16"></span>
        <span v-else class="font-medium tabular-nums">
          {{ current ? formatRate(current.down) : '—' }}
        </span>
      </span>
      <span class="flex items-center gap-1.5">
        <svg width="16" height="4" aria-hidden="true">
          <line x1="0" y1="2" x2="16" y2="2" stroke="var(--chart-up)" stroke-width="2" />
        </svg>
        Up
        <span v-if="skeleton" class="skeleton w-16"></span>
        <span v-else class="font-medium tabular-nums">
          {{ current ? formatRate(current.up) : '—' }}
        </span>
      </span>
      <span v-if="skeleton" class="skeleton ml-auto w-10"></span>
      <button
        v-else
        type="button"
        class="link ml-auto"
        :aria-pressed="table"
        @click="table = !table"
      >
        Table
      </button>
    </div>

    <svg
      v-if="skeleton"
      :width="width"
      :height="HEIGHT"
      :viewBox="`0 0 ${width} ${HEIGHT}`"
      class="block max-w-full"
      aria-hidden="true"
    >
      <line
        v-for="v in rates"
        :key="v"
        :x1="PAD.left"
        :x2="width - PAD.right"
        :y1="Math.round(y(v)) + 0.5"
        :y2="Math.round(y(v)) + 0.5"
        :stroke="v === 0 ? 'var(--line-2)' : 'var(--line)'"
        stroke-width="1"
      />
      <g class="animate-pulse fill-surface-2 motion-reduce:animate-none">
        <rect
          v-for="v in rates"
          :key="v"
          :x="PAD.left - 48"
          :y="y(v) - 6"
          width="40"
          height="12"
          rx="4"
        />
        <rect
          v-for="t in times"
          :key="t"
          :x="x(t) - 22"
          :y="HEIGHT - 17"
          width="44"
          height="12"
          rx="4"
        />
        <path :d="wave" />
      </g>
    </svg>

    <div v-else-if="table" class="max-h-72 overflow-y-auto rounded-md border border-line">
      <table class="table">
        <caption class="sr-only">
          {{
            label
          }}, down and up
        </caption>
        <thead>
          <tr>
            <th>Time</th>
            <th class="text-right">Down</th>
            <th class="text-right">Up</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!shown.length">
            <td colspan="3" class="text-ink-muted">
              {{ points ? 'Nothing counted yet.' : 'Reading…' }}
            </td>
          </tr>
          <tr v-for="p in shown" :key="p[0]">
            <td class="text-xs whitespace-nowrap text-ink-muted tabular-nums">
              {{ pointLabel(p[0], window) }}
            </td>
            <td class="text-right tabular-nums">{{ formatRate(p[1]) }}</td>
            <td class="text-right tabular-nums">{{ formatRate(p[2]) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div
      v-else
      class="relative rounded-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
      tabindex="0"
      :aria-describedby="`${id}-keys`"
      @keydown="key"
      @blur="at = -1"
    >
      <span :id="`${id}-keys`" class="sr-only">Arrow keys move through the points.</span>
      <svg
        ref="svg"
        role="img"
        :aria-label="summary"
        :width="width"
        :height="HEIGHT"
        :viewBox="`0 0 ${width} ${HEIGHT}`"
        class="block max-w-full touch-pan-y select-none"
        :class="{ 'opacity-60': stale }"
        @pointermove="pointer"
        @pointerleave="at = -1"
      >
        <g aria-hidden="true">
          <template v-for="v in rates" :key="v">
            <line
              :x1="PAD.left"
              :x2="width - PAD.right"
              :y1="Math.round(y(v)) + 0.5"
              :y2="Math.round(y(v)) + 0.5"
              :stroke="v === 0 ? 'var(--line-2)' : 'var(--line)'"
              stroke-width="1"
            />
            <text
              :x="PAD.left - 8"
              :y="y(v)"
              text-anchor="end"
              dominant-baseline="middle"
              class="fill-ink-muted text-xs tabular-nums"
            >
              {{ formatRate(v) }}
            </text>
          </template>
          <text
            v-for="t in times"
            :key="t"
            :x="x(t)"
            :y="HEIGHT - 6"
            text-anchor="middle"
            class="fill-ink-muted text-xs tabular-nums"
          >
            {{ timeLabel(t, window) }}
          </text>
          <path
            :d="down"
            fill="none"
            stroke="var(--chart-down)"
            stroke-width="2"
            stroke-linejoin="round"
            stroke-linecap="round"
          />
          <path
            :d="up"
            fill="none"
            stroke="var(--chart-up)"
            stroke-width="2"
            stroke-linejoin="round"
            stroke-linecap="round"
          />
          <template v-if="last && !point">
            <circle
              :cx="x(last[0])"
              :cy="y(last[1])"
              r="4"
              fill="var(--chart-down)"
              stroke="var(--surface)"
              stroke-width="2"
            />
            <circle
              :cx="x(last[0])"
              :cy="y(last[2])"
              r="4"
              fill="var(--chart-up)"
              stroke="var(--surface)"
              stroke-width="2"
            />
          </template>
          <template v-if="point">
            <line
              :x1="x(point[0])"
              :x2="x(point[0])"
              :y1="PAD.top"
              :y2="PAD.top + plotH"
              stroke="var(--edge)"
              stroke-width="1"
            />
            <circle
              :cx="x(point[0])"
              :cy="y(point[1])"
              r="4"
              fill="var(--chart-down)"
              stroke="var(--surface)"
              stroke-width="2"
            />
            <circle
              :cx="x(point[0])"
              :cy="y(point[2])"
              r="4"
              fill="var(--chart-up)"
              stroke="var(--surface)"
              stroke-width="2"
            />
          </template>
        </g>
      </svg>
      <p
        v-if="!points || !shown.length"
        class="pointer-events-none absolute inset-0 flex items-center justify-center pl-16 text-ink-muted"
      >
        {{ points ? 'Nothing counted yet.' : 'Reading…' }}
      </p>
      <div
        v-if="point"
        class="pointer-events-none absolute top-1 rounded-md border border-line bg-surface px-2.5 py-1.5 text-sm shadow-sm"
        :style="readoutStyle"
        aria-live="polite"
      >
        <div class="text-xs text-ink-muted tabular-nums">{{ pointLabel(point[0], window) }}</div>
        <div class="flex items-center gap-1.5">
          <svg width="12" height="4" aria-hidden="true">
            <line x1="0" y1="2" x2="12" y2="2" stroke="var(--chart-down)" stroke-width="2" />
          </svg>
          <span class="font-medium tabular-nums">{{ formatRate(point[1]) }}</span>
          <span class="text-ink-muted">Down</span>
        </div>
        <div class="flex items-center gap-1.5">
          <svg width="12" height="4" aria-hidden="true">
            <line x1="0" y1="2" x2="12" y2="2" stroke="var(--chart-up)" stroke-width="2" />
          </svg>
          <span class="font-medium tabular-nums">{{ formatRate(point[2]) }}</span>
          <span class="text-ink-muted">Up</span>
        </div>
      </div>
    </div>
  </div>
</template>
