<script setup>
import { computed, onMounted, ref } from 'vue'

import ApplyPending from '@/components/ApplyPending.vue'
import { api } from '@/lib/api'
import { useSystemStore } from '@/stores/system'

const system = useSystemStore()
const health = ref(null)
const error = ref('')
const status = computed(() => system.status)

onMounted(async () => {
  try {
    ;[health.value] = await Promise.all([api.health(), system.refresh()])
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Dashboard</h1>
    <p v-if="error || system.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ error || system.error }}
    </p>

    <ApplyPending
      v-if="status?.pending"
      :key="status.pending.deadline"
      :deadline="status.pending.deadline"
      @confirmed="system.refresh()"
      @reverted="system.refresh()"
    />

    <p
      v-if="status && !status.configured"
      class="rounded-lg border border-sky-300 bg-sky-50 p-4 text-sm dark:border-sky-800 dark:bg-sky-950/40"
    >
      This firewall has no configuration yet.
      <RouterLink to="/wizard" class="font-medium underline">Run the setup wizard</RouterLink>.
    </p>

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
