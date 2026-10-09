<script setup>
import { computed, ref, watch } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatCount, formatWhen } from '@/lib/format'
import { FILE_DAYS, FILE_LOG_NAMES } from '@/lib/logs'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()

/**
 * The Logs card's files: whether the logs in memory are written to them,
 * how, and what they hold. Out of the draft while every value is the
 * default, as the saved configuration has it: opening the page must not
 * make a change.
 */
const logging = computed(() => config.draft.system.logging ?? {})
const files = computed(() => logging.value.files ?? {})
function set(key, value) {
  const next = { ...files.value }
  if (value === undefined) delete next[key]
  else next[key] = value
  const log = { ...logging.value }
  if (Object.keys(next).length) log.files = next
  else delete log.files
  if (Object.keys(log).length) config.draft.system.logging = log
  else delete config.draft.system.logging
}

const enabled = computed({
  get: () => Boolean(files.value.enabled),
  set: (on) => set('enabled', on ? true : undefined),
})

const WRITE_EVERY = [
  { value: 1, label: '1 minute' },
  { value: 5, label: '5 minutes' },
  { value: 15, label: '15 minutes' },
  { value: 60, label: '1 hour' },
]
const every = computed({
  get: () => files.value.writeMinutes || 5,
  set: (v) => set('writeMinutes', v === 5 ? undefined : v),
})

/** Empty means the default, which the placeholder shows. */
function numberField(key) {
  return computed({
    get: () => files.value[key] || '',
    set: (v) => set(key, Number.isFinite(v) && v > 0 ? v : undefined),
  })
}
const retention = numberField('retentionDays')
const maxUse = numberField('maxUseGB')

// What the files hold is the router's, read as they are written.
const state = ref(null)
const status = useAsync(
  async () => {
    state.value = await api.logFiles()
  },
  { immediate: true, interval: 10000 },
)
// An apply that switches them on or off is read at once, not at the next
// poll.
watch(
  () => config.saved?.system?.logging?.files?.enabled,
  () => status.run(),
)

// A Clear on the card reads what is left at once, not at the next poll.
defineExpose({ refresh: status.run })

const logs = computed(() => state.value?.logs ?? [])
const name = (l) => FILE_LOG_NAMES[l.name] ?? l.name

/** "4.2 MB in /var/log/ostiole, last written 17:45." */
const line = computed(() => {
  const s = state.value
  if (!s?.enabled) return ''
  const written = logs.value
    .map((l) => l.written)
    .filter(Boolean)
    .sort()
    .at(-1)
  if (!written) return 'Not written yet.'
  const bytes = logs.value.reduce((sum, l) => sum + (l.bytes ?? 0), 0)
  return `${formatBytes(bytes)} in ${s.dir}, last written ${formatWhen(written)}.`
})

/** What each log's files are short of, a sentence each. */
const notes = computed(() => {
  const out = []
  for (const l of logs.value) {
    if (l.overCap)
      out.push(`${name(l)}: today's file is over the cap, so it is written again tomorrow.`)
    if (l.lost)
      out.push(`${name(l)}: ${formatCount(l.lost)} entries left memory before they were written.`)
    if (l.skipped) out.push(`${name(l)}: ${formatCount(l.skipped)} lines could not be read back.`)
    for (const f of l.unknown ?? [])
      out.push(`${name(l)}: ${f} is in a format this version cannot read.`)
  }
  if (state.value?.capped) out.push('The cap, not the days, decides how far back the files go.')
  return out
})
const failures = computed(() => logs.value.filter((l) => l.error))
</script>

<template>
  <fieldset class="field-group">
    <legend>Files</legend>
    <ToggleRow
      v-model="enabled"
      label="Write logs to files"
      hint="Every log that is on, read back when Ostiole starts. The files hold client addresses."
    />
    <div v-if="enabled" class="fields fields-card">
      <FormField
        id="log-files-retention"
        label="Kept for (days)"
        :hint="`${FILE_DAYS.default} is the default, ${FILE_DAYS.max} at most. A log set to fewer days keeps fewer.`"
      >
        <input
          id="log-files-retention"
          v-model.number="retention"
          type="number"
          min="0"
          :max="FILE_DAYS.max"
          :placeholder="String(FILE_DAYS.default)"
          class="input w-32 max-sm:w-full"
        />
      </FormField>
      <FormField
        id="log-files-max-use"
        label="Kept on disk (GB)"
        hint="The oldest day is deleted beyond this."
      >
        <input
          id="log-files-max-use"
          v-model.number="maxUse"
          type="number"
          min="0"
          max="1024"
          placeholder="1"
          class="input w-32 max-sm:w-full"
        />
      </FormField>
      <FormField
        id="log-files-every"
        label="Write every"
        hint="5 minutes is the default. A power cut loses what was not written yet."
      >
        <select id="log-files-every" v-model.number="every" class="input w-48 max-sm:w-full">
          <option v-for="w in WRITE_EVERY" :key="w.value" :value="w.value">{{ w.label }}</option>
        </select>
      </FormField>
    </div>
    <p v-if="line" class="text-xs text-ink-muted">{{ line }}</p>
    <AppNotice v-if="state?.enabled && state.paused">
      The disk has less than 5% free, so the logs wait in memory.
    </AppNotice>
    <AppNotice v-for="l in failures" :key="l.name" kind="bad">
      {{ name(l) }}: {{ l.error }}
    </AppNotice>
    <p v-for="n in notes" :key="n" class="text-ink-muted">{{ n }}</p>
    <ErrorLine v-if="status.error.value" class="text-sm">{{ status.error.value }}</ErrorLine>
  </fieldset>
</template>
