<script setup>
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { FILE_LOG_NAMES } from '@/lib/logs'
import { adminOnly, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useToastStore } from '@/stores/toast'
import LogFilesFields from '@/views/system/LogFilesFields.vue'

const auth = useAuthStore()
const config = useConfigStore()
const toast = useToastStore()
/**
 * Out of the draft while every value is the default, as the saved
 * configuration has it: opening the page must not make a change.
 */
const logging = computed(() => config.draft.system.logging ?? {})
function set(key, value) {
  const next = { ...logging.value }
  if (value === undefined) delete next[key]
  else next[key] = value
  if (Object.keys(next).length) config.draft.system.logging = next
  else delete config.draft.system.logging
}

const LEVELS = [
  { value: 'error', label: 'Error' },
  { value: 'warning', label: 'Warning' },
  { value: 'info', label: 'Info' },
  { value: 'debug', label: 'Debug' },
]

/** An unset level is the default rather than a fifth choice. */
const level = computed(() => logging.value.level || 'warning')

function setLevel(v) {
  set('level', v === 'warning' ? undefined : v)
}

/** Empty means the default, which the placeholder shows. */
function numberField(key) {
  return computed({
    get: () => logging.value[key] || '',
    set: (v) => set(key, Number.isFinite(v) && v > 0 ? v : undefined),
  })
}
const retention = numberField('retentionDays')
const maxUse = numberField('maxUseGB')

/** What Clear every log takes: every log a page clears. */
const CLEARED = Object.values(FILE_LOG_NAMES)

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

const files = ref(null)
const clearAll = useAsync(async () => {
  await api.clearLogs()
  toast.show('Every log is cleared.')
  files.value?.refresh()
})
</script>

<template>
  <SectionCard title="Logs" :locked="auth.readOnly">
    <template #actions>
      <ConfirmButton
        label="Clear every log"
        question="Clear every log?"
        :description="
          inFiles
            ? 'Each is emptied, and its files are deleted. The journal is kept.'
            : 'Each is emptied. The journal is kept.'
        "
        :dependents="CLEARED"
        dependents-label="Cleared"
        :busy="clearAll.busy.value"
        :disabled="!auth.isAdmin"
        :title="auth.isAdmin ? undefined : adminOnly('clear', 'them')"
        @confirm="clearAll.run()"
      />
    </template>
    <div class="space-y-4">
      <ErrorLine v-if="clearAll.error.value">{{ clearAll.error.value }}</ErrorLine>
      <fieldset class="field-group">
        <legend>Journal</legend>
        <fieldset class="space-y-1.5">
          <legend class="group-title mb-1">Level</legend>
          <ToggleRow
            v-for="l in LEVELS"
            :id="`logs-level-${l.value}`"
            :key="l.value"
            :model-value="level"
            variant="radio"
            name="logs-level"
            :value="l.value"
            :label="l.label"
            @update:model-value="setLevel"
          />
          <p class="text-ink-muted">
            Warning is the default. Info records each lease and each wireless client. Changing it
            reconnects wireless clients.
          </p>
        </fieldset>
        <div class="fields fields-card">
          <FormField
            id="logs-retention"
            label="Kept for (days)"
            hint="Entries older than this are deleted."
          >
            <input
              id="logs-retention"
              v-model.number="retention"
              type="number"
              min="0"
              max="3650"
              placeholder="90"
              class="input w-32 max-sm:w-full"
            />
          </FormField>
          <FormField
            id="logs-max-use"
            label="Kept on disk (GB)"
            hint="The oldest entries are deleted beyond this."
          >
            <input
              id="logs-max-use"
              v-model.number="maxUse"
              type="number"
              min="0"
              max="1024"
              placeholder="10"
              class="input w-32 max-sm:w-full"
            />
          </FormField>
        </div>
      </fieldset>
      <LogFilesFields ref="files" />
    </div>
  </SectionCard>
</template>
