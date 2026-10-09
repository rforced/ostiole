<script setup>
import { computed, onMounted, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatCount } from '@/lib/format'
import { DAYS, FILE_LOGS, LOGS, fileDays, fullBytes, totalBytes } from '@/lib/logs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

/**
 * How much of one log is kept: the most entries, which is what it costs in
 * memory, and how many days back. It says what the log costs full, and what
 * every log that is on costs together against what this router has for them.
 */
const props = defineProps({
  /** A key of LOGS: firewall, queries or events. */
  log: { type: String, required: true },
  title: { type: String, required: true },
  intro: { type: String, default: '' },
  /** The log is off, so there is nothing to size. The off slot can say why. */
  off: { type: Boolean, default: false },
})
/** {entries, days}, an empty field being the default. */
const settings = defineModel({ type: Object, required: true })

const auth = useAuthStore()
const config = useConfigStore()
const spec = computed(() => LOGS[props.log])

/** Empty means the default, which the placeholder shows. */
function field(key) {
  return computed({
    get: () => settings.value[key] || '',
    set: (v) => {
      const next = { ...settings.value }
      if (Number.isFinite(v) && v > 0) next[key] = v
      else delete next[key]
      settings.value = next
    },
  })
}
const entries = field('entries')
const days = field('days')

const memory = ref(0)
const stats = useAsync(async () => {
  memory.value = (await api.systemStats()).memTotal ?? 0
})
/** The budget and ceilings this router's memory sets, null until the daemon says. */
const limits = ref(null)
const limitsLoad = useAsync(async () => {
  limits.value = await api.logLimits()
})
/** What this log's files hold, while System → General writes them. */
const inFiles = ref(null)
const files = useAsync(async () => {
  const st = await api.logFiles()
  inFiles.value = st.enabled ? (st.logs ?? []).find((l) => l.name === props.log) : null
})
onMounted(() => {
  stats.run()
  limitsLoad.run()
  if (FILE_LOGS.includes(props.log)) files.run()
})

const cost = computed(() => formatBytes(fullBytes(props.log, entries.value)))
/** The most entries this log may keep here. */
const ceiling = computed(() => limits.value?.ceilings?.[props.log] || spec.value.max)
/** How many days the files keep this log, 0 while they are off. */
const kept = computed(() => fileDays(config.draft, props.log, days.value))
const entriesHint = computed(() => {
  const older = kept.value ? 'Older entries stay in the files.' : 'Older entries are dropped.'
  const base = `${formatCount(spec.value.entries)} is the default. In memory, about ${cost.value} when full. ${older}`
  return ceiling.value < spec.value.max
    ? `${base} This router allows up to ${formatCount(ceiling.value)}.`
    : base
})
const total = computed(() => totalBytes(config.draft))
/** What the logs may cost at their largest here, null while unknown. */
const budget = computed(() => {
  const l = limits.value
  return l?.memTotal > 0 && Number.isFinite(l.budget) ? l.budget : null
})
const outOf = computed(() => (memory.value ? ` of ${formatBytes(memory.value)}` : ''))
/** What the logs cost at their largest, which is what the daemon checks. */
const peak = computed(() => total.value * (limits.value?.peakFactor || 1))
const over = computed(() => budget.value !== null && peak.value > budget.value)
const heavy = computed(
  () => budget.value === null && memory.value > 0 && total.value > memory.value / 2,
)
const daysHint = computed(() => {
  const base = `${DAYS.default} is the default, ${DAYS.max} at most.`
  return kept.value ? `${base} Files keep ${kept.value} days.` : base
})
</script>

<template>
  <SectionCard :title="title" :intro="intro" :locked="auth.readOnly">
    <template v-if="$slots.actions" #actions><slot name="actions" /></template>
    <div class="space-y-4">
      <slot />
      <template v-if="!off">
        <div class="fields fields-card">
          <FormField :id="`${log}-entries`" label="Entries" :hint="entriesHint">
            <input
              :id="`${log}-entries`"
              v-model.number="entries"
              type="number"
              min="0"
              :max="ceiling"
              :placeholder="String(spec.entries)"
              class="input w-32 max-sm:w-full"
            />
          </FormField>
          <FormField :id="`${log}-days`" label="Days" :hint="daysHint">
            <input
              :id="`${log}-days`"
              v-model.number="days"
              type="number"
              min="0"
              :max="DAYS.max"
              :placeholder="String(DAYS.default)"
              class="input w-32 max-sm:w-full"
            />
          </FormField>
        </div>
        <p v-if="budget !== null" class="text-ink-muted">
          All logs in memory: {{ formatBytes(peak) }} at their largest. This router has
          {{ formatBytes(budget) }} for them.
        </p>
        <p v-else class="text-ink-muted">
          All logs in memory: {{ formatBytes(total) }}{{ outOf }} when full.
        </p>
        <p v-if="inFiles" class="text-ink-muted">In files: {{ formatBytes(inFiles.bytes) }}.</p>
        <AppNotice v-if="over">That is more than this router has for them.</AppNotice>
        <AppNotice v-else-if="heavy">That is more than half of this router's memory.</AppNotice>
      </template>
      <p v-else class="text-ink-muted"><slot name="off">Off.</slot></p>
    </div>
  </SectionCard>
</template>
