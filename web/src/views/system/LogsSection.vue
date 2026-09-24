<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
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
</script>

<template>
  <SectionCard title="Logs" :locked="auth.readOnly">
    <div class="space-y-4">
      <fieldset class="space-y-1.5">
        <legend class="group-title mb-1 block">Level</legend>
        <div v-for="l in LEVELS" :key="l.value" class="flex items-start gap-2">
          <input
            :id="`logs-level-${l.value}`"
            type="radio"
            class="mt-1"
            name="logs-level"
            :value="l.value"
            :checked="level === l.value"
            @change="setLevel(l.value)"
          />
          <label :for="`logs-level-${l.value}`">{{ l.label }}</label>
        </div>
        <p class="text-ink-muted">
          Warning is the default. Info records each lease and each wireless client. Changing it
          reconnects wireless clients.
        </p>
      </fieldset>
      <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
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
    </div>
  </SectionCard>
</template>
