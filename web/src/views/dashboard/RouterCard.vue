<script setup>
import SectionCard from '@/components/SectionCard.vue'
defineProps({
  /** Engine status from GET /status. */
  status: { type: Object, default: null },
  /** The status summary from GET /overview: hostname and counts. */
  summary: { type: Object, default: () => ({}) },
  /** GET /health: what is running. */
  health: { type: Object, default: null },
  /** A newer release, when the nightly check found one. */
  update: { type: Object, default: null },
  /** False until the first overview arrives; the counts are zero until then. */
  loaded: { type: Boolean, default: true },
})

/** The rows the card will have, with about the width each value takes. */
const READING = [
  ['Hostname', 'w-20'],
  ['Version', 'w-32'],
  ['Ruleset', 'w-52'],
  ['Network backend', 'w-20'],
  ['Revisions', 'w-44'],
]
</script>

<template>
  <SectionCard title="Router" :aria-busy="status && loaded ? undefined : 'true'">
    <dl v-if="status && loaded" class="kv">
      <dt>Hostname</dt>
      <dd class="font-mono">{{ summary.hostname || '—' }}</dd>

      <dt>Version</dt>
      <dd>
        <span class="font-mono">{{ health?.version ?? '—' }}</span>
        <span
          v-if="health?.commit && health.commit !== 'none'"
          class="ml-1 font-mono text-ink-muted"
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
    <template v-else>
      <p class="sr-only">Reading…</p>
      <dl class="kv" aria-hidden="true" data-reading>
        <template v-for="[label, width] in READING" :key="label">
          <dt>{{ label }}</dt>
          <dd><span class="skeleton" :class="width"></span></dd>
        </template>
      </dl>
    </template>
  </SectionCard>
</template>
