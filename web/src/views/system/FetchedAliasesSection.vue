<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatCount } from '@/lib/format'
import type { LogLimitsInfo } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()

/** Match model.DefaultMaxAliasEntries, model.AliasEntriesCeiling and model.AliasEntryBytes. */
const DEFAULT_MAX = 2000000
const CEILING = 5000000
const BYTES_PER_ENTRY = 600

/** The most this router's memory allows, null until the daemon says. */
const limits = ref<LogLimitsInfo | null>(null)
const limitsLoad = useAsync(async () => {
  limits.value = await api.logLimits()
})
onMounted(limitsLoad.run)
const ceiling = computed(() => limits.value?.aliasEntries || CEILING)
/** A router short of memory lowers the default to what it allows. */
const defaultHere = computed(() => Math.min(DEFAULT_MAX, ceiling.value))

/** Out of the draft when empty, as the saved configuration has it. */
function setKey(value: string) {
  if (!config.draft) return
  const key = value.trim()
  if (key) config.draft.system.maxmindLicenceKey = key
  else delete config.draft.system.maxmindLicenceKey
}

/** Empty or the default leaves the setting out, which the placeholder shows. */
const maxEntries = computed({
  get: () => config.draft?.system.maxAliasEntries || '',
  set: (v) => {
    if (!config.draft) return
    const n = Math.trunc(Number(v))
    if (!n || n === DEFAULT_MAX) delete config.draft.system.maxAliasEntries
    else config.draft.system.maxAliasEntries = n
  },
})

const entriesHint = computed(() => {
  const bytes = formatBytes((maxEntries.value || defaultHere.value) * BYTES_PER_ENTRY)
  const most = limits.value ? `, at most ${formatCount(ceiling.value)} on this router` : ''
  return `About ${bytes} of memory while a refresh reloads that many. Default ${formatCount(defaultHere.value)}${most}.`
})
</script>

<template>
  <SectionCard title="Fetched aliases" :locked="auth.readOnly">
    <div class="fields fields-card">
      <FormField
        id="fa-maxmind-key"
        label="MaxMind licence key"
        hint="With a key, each address counts where it is used rather than where it is registered, and MaxMind sees this router's address at each download."
      >
        <input
          id="fa-maxmind-key"
          type="password"
          class="input font-mono"
          autocomplete="new-password"
          spellcheck="false"
          :value="config.draft?.system.maxmindLicenceKey ?? ''"
          @change="setKey(($event.target as HTMLInputElement).value)"
        />
      </FormField>
      <FormField id="fa-max-entries" label="Most entries per alias" :hint="entriesHint">
        <input
          id="fa-max-entries"
          v-model.number="maxEntries"
          type="number"
          min="1000"
          :max="ceiling"
          step="1000"
          :placeholder="String(defaultHere)"
          class="input w-32 font-mono max-sm:w-full"
        />
      </FormField>
      <p v-if="config.draft?.system.maxmindLicenceKey" class="text-sm text-ink-muted">
        This product includes GeoLite Data created by MaxMind, available from
        https://www.maxmind.com.
      </p>
    </div>
  </SectionCard>
</template>
