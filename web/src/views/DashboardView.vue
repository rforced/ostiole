<script setup>
import { onMounted, ref } from 'vue'

import { api } from '@/lib/api'

/** @type {import('vue').Ref<import('@/lib/api').Health | null>} */
const health = ref(null)
const error = ref(null)

onMounted(async () => {
  try {
    health.value = await api.health()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Dashboard</h1>

    <section
      class="rounded-lg border border-neutral-200 p-4 text-sm dark:border-neutral-800"
      aria-labelledby="backend-status"
    >
      <h2 id="backend-status" class="mb-2 font-medium">Backend</h2>
      <p v-if="error" class="text-red-600 dark:text-red-400">Unreachable: {{ error }}</p>
      <dl v-else-if="health" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
        <dt class="text-neutral-500">Status</dt>
        <dd>{{ health.status }}</dd>
        <dt class="text-neutral-500">Version</dt>
        <dd class="font-mono">{{ health.version }}</dd>
        <dt class="text-neutral-500">Commit</dt>
        <dd class="font-mono">{{ health.commit }}</dd>
      </dl>
      <p v-else class="text-neutral-500">Loading…</p>
    </section>
  </div>
</template>
