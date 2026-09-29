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
 * every log that is on costs together against this router's memory.
 */
const props = defineProps({
  /** A key of LOGS: firewall, queries or events. */
  log: { type: String, required: true },
  title: { type: String, required: true },
  intro: { type: String, default: '' },
  /** The log is off, so there is nothing to size. */
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
/** What this log's files hold, while System → General writes them. */
const inFiles = ref(null)
const files = useAsync(async () => {
  const st = await api.logFiles()
  inFiles.value = st.enabled ? (st.logs ?? []).find((l) => l.name === props.log) : null
})
onMounted(() => {
  stats.run()
  if (FILE_LOGS.includes(props.log)) files.run()
})

const cost = computed(() => formatBytes(fullBytes(props.log, entries.value)))
const total = computed(() => totalBytes(config.draft))
const heavy = computed(() => memory.value > 0 && total.value > memory.value / 2)
/** How many days the files keep this log, 0 while they are off. */
const kept = computed(() => fileDays(config.draft, props.log, days.value))
const daysHint = computed(() => {
  const base = `${DAYS.default} is the default, ${DAYS.max} at most.`
  return kept.value
    ? `${base} Files keep ${kept.value} days.`
    : `${base} Older entries are dropped.`
})
</script>

<template>
  <SectionCard :title="title" :intro="intro" :locked="auth.readOnly">
    <template v-if="$slots.actions" #actions><slot name="actions" /></template>
    <div class="space-y-4">
      <slot />
      <template v-if="!off">
        <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
          <FormField
            :id="`${log}-entries`"
            label="Entries"
            :hint="`${formatCount(spec.entries)} is the default. About ${cost} of memory when full.`"
          >
            <input
              :id="`${log}-entries`"
              v-model.number="entries"
              type="number"
              min="0"
              :max="spec.max"
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
        <p class="text-ink-muted">
          All logs: {{ formatBytes(total)
          }}<template v-if="memory"> of {{ formatBytes(memory) }}</template> when full.
        </p>
        <p v-if="inFiles" class="text-ink-muted">In files: {{ formatBytes(inFiles.bytes) }}.</p>
        <AppNotice v-if="heavy">That is more than half of this router's memory.</AppNotice>
      </template>
      <p v-else class="text-ink-muted">Off.</p>
    </div>
  </SectionCard>
</template>
