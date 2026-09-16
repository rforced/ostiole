<script setup>
import { ref } from 'vue'

import { ApiError, api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const text = ref('')
const which = ref('')
const error = ref('')

async function showConfirmed() {
  error.value = ''
  try {
    text.value = await api.ruleset()
    which.value = 'confirmed ruleset'
  } catch (e) {
    error.value = e instanceof ApiError && e.status === 404 ? 'Nothing confirmed yet.' : String(e)
  }
}

async function showDraft() {
  error.value = ''
  try {
    text.value = (await api.config.check(config.draft)).ruleset
    which.value = 'draft ruleset'
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

/**
 * Shows the network units the draft would install: the answer to "why is
 * my bridge not coming up" lives in these files, not in the ruleset.
 */
async function showNetwork() {
  error.value = ''
  try {
    const files = (await api.config.check(config.draft)).network ?? {}
    const names = Object.keys(files).sort()
    if (!names.length) {
      text.value = ''
      error.value = 'This build is managing no network units.'
      return
    }
    text.value = names.map((n) => `==> ${n} <==\n${files[n]}`).join('\n')
    which.value = 'draft network units'
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <section class="card space-y-3" aria-labelledby="rs-title">
    <h2 id="rs-title" class="card-title">Rendered configuration</h2>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn-secondary" @click="showConfirmed">
        Show confirmed ruleset
      </button>
      <button type="button" class="btn-secondary" :disabled="!config.draft" @click="showDraft">
        Render the draft
      </button>
      <button type="button" class="btn-secondary" :disabled="!config.draft" @click="showNetwork">
        Render network units
      </button>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <pre
      v-if="text"
      class="max-h-96 overflow-auto rounded-md bg-neutral-950 p-3 font-mono text-xs text-neutral-100"
      :aria-label="which"
      >{{ text }}</pre>
  </section>
</template>
