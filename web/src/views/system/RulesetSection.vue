<script setup>
import { ref } from 'vue'

import SectionCard from '@/components/SectionCard.vue'
import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const text = ref('')
const which = ref('')
/** Which of the three is being fetched, so only that button reads as busy. */
const rendering = ref('')

async function confirmedRuleset() {
  try {
    return await api.ruleset()
  } catch (e) {
    throw e instanceof ApiError && e.status === 404 ? new Error('Nothing confirmed yet.') : e
  }
}

async function draftRuleset() {
  return (await api.config.check(config.draft)).ruleset
}

/**
 * The network units the draft would install: the answer to "why is my
 * bridge not coming up" lives in these files, not in the ruleset.
 */
async function networkUnits() {
  const files = (await api.config.check(config.draft)).network ?? {}
  const names = Object.keys(files).sort()
  if (!names.length) throw new Error('This build is managing no network units.')
  return names.map((n) => `==> ${n} <==\n${files[n]}`).join('\n')
}

const SOURCES = {
  'confirmed ruleset': confirmedRuleset,
  'draft ruleset': draftRuleset,
  'draft network units': networkUnits,
}

const render = useAsync(async (what) => {
  rendering.value = what
  try {
    text.value = await SOURCES[what]()
    which.value = what
  } finally {
    rendering.value = ''
  }
})
</script>

<template>
  <SectionCard title="Rendered configuration">
    <template #actions>
      <button
        type="button"
        class="btn-secondary"
        :disabled="render.busy.value"
        :aria-busy="rendering === 'confirmed ruleset'"
        @click="render.run('confirmed ruleset')"
      >
        Show confirmed ruleset
      </button>
      <button
        type="button"
        class="btn-secondary"
        :disabled="!config.draft || render.busy.value"
        :aria-busy="rendering === 'draft ruleset'"
        @click="render.run('draft ruleset')"
      >
        Render the draft
      </button>
      <button
        type="button"
        class="btn-secondary"
        :disabled="!config.draft || render.busy.value"
        :aria-busy="rendering === 'draft network units'"
        @click="render.run('draft network units')"
      >
        Render network units
      </button>
    </template>
    <div class="space-y-3">
      <p v-if="render.error.value" role="alert" class="text-bad">{{ render.error.value }}</p>
      <!-- The one black block in the product: a terminal is what this is. -->
      <pre
        v-if="text"
        class="max-h-96 overflow-auto rounded-md bg-neutral-950 p-3 font-mono text-code text-neutral-100"
        :aria-label="which"
        >{{ text }}</pre>
    </div>
  </SectionCard>
</template>
