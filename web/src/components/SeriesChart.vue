<script setup>
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue'

import {
  SPANS,
  bandPath,
  fittingTicks,
  nearest,
  niceScale,
  pointLabel,
  rateTicks,
  seriesPath,
  timeLabel,
  timeTicks,
} from '@/lib/chart'

/**
 * Values over a window: lines on one axis, each with an optional low to
 * high band, and bars of a share on a second axis from 0 to 100. The
 * legend keys each and says its value now; a crosshair snaps to the
 * nearest point under the pointer or the arrow keys and reads every value
 * there; Table shows the points as rows. A missing value, or points
 * further apart than gap seconds, leave a gap rather than a zero.
 */
const props = defineProps({
  /** [seconds, ...values], oldest first; null until the first read. */
  points: { type: Array, default: null },
  /** A key of SPANS: 5m, 24h or 30d. */
  window: { type: String, default: '5m' },
  /** The right edge in seconds; the last point's time when unset. */
  end: { type: Number, default: 0 },
  /** What is charted, as the page names it. */
  label: { type: String, required: true },
  /** The lines: {index, name, color, band?: [low index, high index]}. */
  series: { type: Array, required: true },
  /** The bars: {index, name, color}, a share from 0 to 100. */
  bars: { type: Array, default: () => [] },
  /** How a value on the lines' axis reads. */
  format: { type: Function, required: true },
  /** The lines' axis for the highest value shown. */
  scale: { type: Function, default: (highest) => niceScale(highest) },
  /** Each line's value now, in series order; the last point's when unset. */
  now: { type: Array, default: null },
  /** Seconds between points beyond which a line breaks; 0 never breaks. */
  gap: { type: Number, default: 0 },
  /** Seconds a bar covers. */
  step: { type: Number, default: 0 },
  /** What an empty chart says. */
  empty: { type: String, default: 'Nothing counted yet.' },
  /** A new window is being read, and this is still the old one. */
  stale: { type: Boolean, default: false },
  /** Nothing is read yet: grey where the values, the times and the lines go. */
  skeleton: { type: Boolean, default: false },
})

/** The plot and the time axis under it, in pixels. */
const HEIGHT = 168
/** The traffic a skeleton draws, as fractions of the plot's height. */
const WAVE = [0.3, 0.42, 0.36, 0.5, 0.44, 0.58, 0.4, 0.48, 0.34, 0.46, 0.38]
const SHARES = [0, 50, 100]

const PAD = computed(() => ({ top: 10, right: props.bars.length ? 48 : 12, bottom: 24, left: 76 }))
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
  for (const p of shown.value) {
    for (const s of props.series) highest = Math.max(highest, p[s.index] ?? 0, p[s.band?.[1]] ?? 0)
  }
  return props.scale(highest)
})

const plotW = computed(() => width.value - PAD.value.left - PAD.value.right)
const plotH = HEIGHT - 10 - 24
const x = (t) => PAD.value.left + ((t - startT.value) / span.value) * plotW.value
const y = (v) => PAD.value.top + plotH - (v / scale.value.top) * plotH
const yShare = (v) => PAD.value.top + plotH - (v / 100) * plotH

const lines = computed(() =>
  props.series.map((s) => ({
    ...s,
    d: seriesPath(shown.value, s.index, x, y, props.gap),
    area: s.band ? bandPath(shown.value, s.band[0], s.band[1], x, y, props.gap) : '',
  })),
)
const barW = computed(() =>
  props.bars.length
    ? Math.max(1, (plotW.value * props.step) / span.value / props.bars.length - 1)
    : 0,
)
const columns = computed(() =>
  props.bars.map((b, k) => ({
    ...b,
    rects: shown.value
      .filter((p) => p[b.index] > 0)
      .map((p) => ({
        t: p[0],
        x: x(p[0]) + k * (barW.value + 1),
        y: yShare(p[b.index]),
        h: (p[b.index] / 100) * plotH,
      })),
  })),
)
const rates = computed(() => rateTicks(scale.value))
const times = computed(() =>
  fittingTicks(
    timeTicks(startT.value, endT.value, props.window, width.value < 480),
    x,
    (t) => timeLabel(t, props.window),
    width.value,
  ),
)
const last = computed(() => shown.value.at(-1) ?? null)
const current = computed(
  () => props.now ?? (last.value ? props.series.map((s) => last.value[s.index]) : null),
)
const share = (v) => `${Math.round(v)}%`
const valueOf = (v) => (v == null ? '—' : props.format(v))

/** The skeleton's traffic: an area over the zero line, curved through WAVE. */
const wave = computed(() => {
  const base = PAD.value.top + plotH
  const step = plotW.value / (WAVE.length - 1)
  const p = WAVE.map((f, i) => [PAD.value.left + i * step, base - f * plotH])
  const at = ([px, py]) => `${px.toFixed(1)},${py.toFixed(1)}`
  let d = `M${at([PAD.value.left, base])}L${at(p[0])}`
  for (let i = 1; i < p.length - 1; i++) {
    d += `Q${at(p[i])} ${at([(p[i][0] + p[i + 1][0]) / 2, (p[i][1] + p[i + 1][1]) / 2])}`
  }
  return `${d}L${at(p.at(-1))}L${at([p.at(-1)[0], base])}Z`
})

const summary = computed(() => {
  if (!props.points) return `${props.label}: reading`
  if (!current.value) return `${props.label}: ${props.empty.replace(/\.$/, '').toLowerCase()}`
  const values = props.series.map((s, i) => `${s.name.toLowerCase()} ${valueOf(current.value[i])}`)
  return `${props.label}: ${values.join(', ')}`
})
const caption = computed(() => {
  const names = [...props.series, ...props.bars].map((s) => s.name.toLowerCase())
  return `${props.label}, ${names.slice(0, -1).join(', ')}${names.length > 1 ? ' and ' : ''}${names.at(-1)}`
})

/** The point the crosshair is on, as an index into shown, or -1. */
const at = ref(-1)
const point = computed(() => (at.value >= 0 ? (shown.value[at.value] ?? null) : null))

function pointer(e) {
  const rect = svg.value?.getBoundingClientRect()
  if (!rect?.width || !shown.value.length) return
  const px = ((e.clientX - rect.left) / rect.width) * width.value
  at.value = nearest(shown.value, startT.value + ((px - PAD.value.left) / plotW.value) * span.value)
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

/** A band's low and high at a point, as its readout and its row say them. */
function range(p, s) {
  if (!s.band || p[s.band[0]] == null || p[s.band[1]] == null) return ''
  if (p[s.band[0]] === p[s.band[1]]) return ''
  return `${props.format(p[s.band[0]])} to ${props.format(p[s.band[1]])}`
}

const table = ref(false)
</script>

<template>
  <div ref="box" class="min-w-0 space-y-2">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
      <span v-for="(s, i) in series" :key="s.index" class="flex items-center gap-1.5">
        <svg width="16" height="4" aria-hidden="true">
          <line x1="0" y1="2" x2="16" y2="2" :stroke="s.color" stroke-width="2" />
        </svg>
        {{ s.name }}
        <span v-if="skeleton" class="skeleton w-16"></span>
        <span v-else class="font-medium tabular-nums">
          {{ current ? valueOf(current[i]) : '—' }}
        </span>
      </span>
      <span v-for="b in bars" :key="b.index" class="flex items-center gap-1.5">
        <svg width="10" height="10" aria-hidden="true">
          <rect width="10" height="10" rx="2" :fill="b.color" />
        </svg>
        {{ b.name }}
        <span v-if="skeleton" class="skeleton w-10"></span>
        <span v-else class="font-medium tabular-nums">
          {{ last && last[b.index] != null ? share(last[b.index]) : '—' }}
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
            caption
          }}
        </caption>
        <thead>
          <tr>
            <th>Time</th>
            <th v-for="s in series" :key="s.index" class="num">{{ s.name }}</th>
            <th v-for="b in bars" :key="b.index" class="num">{{ b.name }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!shown.length">
            <td :colspan="1 + series.length + bars.length" class="text-ink-muted">
              {{ points ? empty : 'Reading…' }}
            </td>
          </tr>
          <tr v-for="p in shown" :key="p[0]">
            <td class="when">
              {{ pointLabel(p[0], window) }}
            </td>
            <template v-for="s in series" :key="s.index">
              <td v-if="s.band" class="num">
                {{ valueOf(p[s.index]) }}
                <div v-if="range(p, s)" class="text-xs text-ink-muted">{{ range(p, s) }}</div>
              </td>
              <td v-else class="num">{{ valueOf(p[s.index]) }}</td>
            </template>
            <td v-for="b in bars" :key="b.index" class="num">
              {{ p[b.index] == null ? '—' : share(p[b.index]) }}
            </td>
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
              {{ format(v) }}
            </text>
          </template>
          <template v-if="bars.length">
            <text
              v-for="v in SHARES"
              :key="v"
              :x="width - PAD.right + 8"
              :y="yShare(v)"
              dominant-baseline="middle"
              class="fill-ink-muted text-xs tabular-nums"
            >
              {{ share(v) }}
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
          <template v-for="c in columns" :key="c.index">
            <rect
              v-for="r in c.rects"
              :key="r.t"
              :x="r.x"
              :y="r.y"
              :width="barW"
              :height="r.h"
              :fill="c.color"
              data-bar
            />
          </template>
          <template v-for="l in lines" :key="l.index">
            <path v-if="l.area" :d="l.area" :fill="l.color" fill-opacity="0.2" data-band />
            <path
              :d="l.d"
              fill="none"
              :stroke="l.color"
              stroke-width="2"
              stroke-linejoin="round"
              stroke-linecap="round"
            />
          </template>
          <template v-if="last && !point">
            <template v-for="s in series" :key="s.index">
              <circle
                v-if="last[s.index] != null"
                :cx="x(last[0])"
                :cy="y(last[s.index])"
                r="4"
                :fill="s.color"
                stroke="var(--surface)"
                stroke-width="2"
              />
            </template>
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
            <template v-for="s in series" :key="s.index">
              <circle
                v-if="point[s.index] != null"
                :cx="x(point[0])"
                :cy="y(point[s.index])"
                r="4"
                :fill="s.color"
                stroke="var(--surface)"
                stroke-width="2"
              />
            </template>
          </template>
        </g>
      </svg>
      <p
        v-if="!points || !shown.length"
        class="pointer-events-none absolute inset-0 flex items-center justify-center pl-16 text-ink-muted"
      >
        {{ points ? empty : 'Reading…' }}
      </p>
      <div
        v-if="point"
        class="pointer-events-none absolute top-1 rounded-md border border-line bg-surface px-2.5 py-1.5 text-sm shadow-sm"
        :style="readoutStyle"
        aria-live="polite"
      >
        <div class="text-xs text-ink-muted tabular-nums">{{ pointLabel(point[0], window) }}</div>
        <div v-for="s in series" :key="s.index" class="flex items-center gap-1.5">
          <svg width="12" height="4" aria-hidden="true">
            <line x1="0" y1="2" x2="12" y2="2" :stroke="s.color" stroke-width="2" />
          </svg>
          <span class="font-medium tabular-nums">{{ valueOf(point[s.index]) }}</span>
          <span class="text-ink-muted">{{ s.name }}</span>
          <span v-if="range(point, s)" class="text-xs text-ink-muted tabular-nums">
            {{ range(point, s) }}
          </span>
        </div>
        <div v-for="b in bars" :key="b.index" class="flex items-center gap-1.5">
          <svg width="12" height="12" aria-hidden="true">
            <rect width="12" height="12" rx="2" :fill="b.color" />
          </svg>
          <span class="font-medium tabular-nums">
            {{ point[b.index] == null ? '—' : share(point[b.index]) }}
          </span>
          <span class="text-ink-muted">{{ b.name }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
