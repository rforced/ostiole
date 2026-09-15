<script setup>
import { onMounted, ref } from 'vue'

import { api } from '@/lib/api'

const health = ref(null)
const status = ref(null)
const error = ref('')

onMounted(async () => {
  try {
    ;[health.value, status.value] = await Promise.all([api.health(), api.status()])
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Dashboard</h1>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <div class="grid gap-4 sm:grid-cols-2">
      <section class="card" aria-labelledby="fw-status">
        <h2 id="fw-status" class="card-title">Firewall</h2>
        <dl v-if="status" class="kv">
          <dt>Configured</dt>
          <dd>{{ status.configured ? 'yes' : 'no' }}</dd>
          <dt>Ruleset loaded</dt>
          <dd>{{ status.tableLoaded ? 'yes' : 'no' }}</dd>
          <dt>Network backend</dt>
          <dd>{{ status.network }}</dd>
          <dt>Pending apply</dt>
          <dd>
            {{
              status.pending
                ? `until ${new Date(status.pending.deadline).toLocaleTimeString()}`
                : 'none'
            }}
          </dd>
        </dl>
        <p v-else class="text-neutral-500">Loading…</p>
      </section>

      <section class="card" aria-labelledby="backend-status">
        <h2 id="backend-status" class="card-title">Backend</h2>
        <dl v-if="health" class="kv">
          <dt>Status</dt>
          <dd>{{ health.status }}</dd>
          <dt>Version</dt>
          <dd class="font-mono">{{ health.version }}</dd>
          <dt>Commit</dt>
          <dd class="font-mono">{{ health.commit }}</dd>
        </dl>
        <p v-else class="text-neutral-500">Loading…</p>
      </section>
    </div>
  </div>
</template>
