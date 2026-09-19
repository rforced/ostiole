<script setup>
defineProps({
  /** Engine status from GET /status. */
  status: { type: Object, default: null },
  /** The status summary from GET /overview: hostname and counts. */
  summary: { type: Object, default: () => ({}) },
  /** GET /health: what is running. */
  health: { type: Object, default: null },
  /** A newer release, when the nightly check found one. */
  update: { type: Object, default: null },
})
</script>

<template>
  <section class="card" aria-labelledby="dash-router">
    <h2 id="dash-router" class="card-title">Router</h2>
    <dl v-if="status" class="kv">
      <dt>Hostname</dt>
      <dd class="font-mono">{{ summary.hostname || '—' }}</dd>

      <dt>Version</dt>
      <dd>
        <span class="font-mono">{{ health?.version ?? '—' }}</span>
        <span
          v-if="health?.commit && health.commit !== 'none'"
          class="ml-1 font-mono text-neutral-500"
        >
          {{ health.commit }}
        </span>
      </dd>

      <template v-if="update">
        <dt>Update</dt>
        <dd>
          <span class="font-mono">{{ update.latest }}</span> available ·
          <RouterLink to="/system/updates" class="link">install under System</RouterLink>
        </dd>
      </template>

      <dt>Ruleset</dt>
      <dd>
        <span class="badge" :class="status.tableLoaded ? 'badge-ok' : 'badge-warn'">
          {{ status.tableLoaded ? 'loaded' : 'not loaded' }}
        </span>
        <span class="ml-2">
          {{ summary.rules ?? 0 }} rule{{ (summary.rules ?? 0) === 1 ? '' : 's' }} in
          {{ summary.zones ?? 0 }} zone{{ (summary.zones ?? 0) === 1 ? '' : 's' }} ·
          <RouterLink to="/firewall/rules" class="link">edit</RouterLink>
        </span>
      </dd>

      <dt>Network backend</dt>
      <dd>{{ status.network }}</dd>

      <dt>Revisions</dt>
      <dd>
        {{ summary.revisions ?? 0 }} ·
        <RouterLink to="/system/backup" class="link">roll back under System</RouterLink>
      </dd>
    </dl>
    <p v-else class="text-neutral-500">Loading…</p>
  </section>
</template>
