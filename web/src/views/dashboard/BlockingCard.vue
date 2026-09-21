<script setup>
import { computed } from 'vue'

import SectionCard from '@/components/SectionCard.vue'
import { formatCount } from '@/lib/format'

/**
 * DNS blocking, as far as the dashboard can tell: what is loaded, what it
 * costs, when it last changed, and how much has actually been refused
 * while the query log is on.
 */
const props = defineProps({
  /** The blocking summary from GET /overview. */
  blocking: { type: Object, default: () => ({}) },
})

const b = computed(() => props.blocking ?? {})
const when = computed(() =>
  b.value.mergedAt ? new Date(b.value.mergedAt).toLocaleString() : 'never',
)
const queriesSince = computed(() =>
  b.value.queries?.since ? new Date(b.value.queries.since).toLocaleString() : '',
)
const blockedPct = computed(() => {
  const q = b.value.queries
  if (!q?.total) return 0
  return Math.round((q.blocked / q.total) * 100)
})
</script>

<template>
  <SectionCard title="DNS blocking">
    <dl class="kv">
      <dt>State</dt>
      <dd>
        <span v-if="b.active" class="badge badge-ok">blocking</span>
        <span v-else-if="b.enabled" class="badge badge-warn">on, not answering</span>
        <span v-else class="badge">off</span>
        <span v-if="b.enabled && b.mode" class="ml-2 text-ink-muted">{{ b.mode }}</span>
      </dd>

      <template v-if="b.enabled">
        <dt>Names</dt>
        <dd>
          <template v-if="b.blocked">
            {{ formatCount(b.blocked) }} loaded
            <span v-if="b.memoryMb" class="text-ink-muted">· about {{ b.memoryMb }} MB</span>
          </template>
          <span v-else class="text-ink-muted">nothing merged yet</span>
        </dd>

        <dt>Lists</dt>
        <dd>
          {{ b.lists ?? 0 }} subscribed
          <span v-if="b.stale" class="badge badge-warn ml-2">stale</span>
        </dd>

        <dt>Last merge</dt>
        <dd>{{ when }}</dd>
      </template>

      <template v-if="b.queries">
        <dt>Queries</dt>
        <dd>
          {{ formatCount(b.queries.total) }} since {{ queriesSince }} ·
          {{ formatCount(b.queries.blocked) }} blocked ({{ blockedPct }}%)
        </dd>
      </template>

      <template v-if="b.enabled || b.allow || b.deny">
        <dt>Exceptions</dt>
        <dd>
          <span v-if="b.allow || b.deny">
            {{ b.allow ?? 0 }} allowed · {{ b.deny ?? 0 }} refused
          </span>
          <span v-else class="text-ink-muted">none</span>
        </dd>
      </template>
    </dl>
    <p v-if="!b.enabled" class="mt-2 text-ink-muted">
      Block lists are off. Turn them on under
      <RouterLink class="link" to="/services/dns#blocking">Services, DNS, Blocking</RouterLink>.
    </p>
  </SectionCard>
</template>
