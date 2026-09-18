<script setup>
import { computed } from 'vue'

import { formatCount } from '@/lib/format'

/**
 * DNS blocking, as far as the dashboard can tell: what is loaded, what it
 * costs, and when it last changed.
 *
 * There is no query count or block rate here. Both would have to be read
 * out of dnsmasq's own log, which nothing on this router does yet, and a
 * card that showed a rate of zero would be worse than a card that does
 * not claim to know.
 */
const props = defineProps({
  /** The blocking summary from GET /overview. */
  blocking: { type: Object, default: () => ({}) },
})

const b = computed(() => props.blocking ?? {})
const when = computed(() =>
  b.value.mergedAt ? new Date(b.value.mergedAt).toLocaleString() : 'never',
)
</script>

<template>
  <section class="card" aria-labelledby="dash-blocking">
    <h2 id="dash-blocking" class="card-title">DNS blocking</h2>
    <dl class="kv">
      <dt>State</dt>
      <dd>
        <span v-if="b.active" class="badge badge-ok">blocking</span>
        <span v-else-if="b.enabled" class="badge badge-warn">on, not answering</span>
        <span v-else class="badge">off</span>
        <span v-if="b.enabled && b.mode" class="ml-2 text-neutral-500">{{ b.mode }}</span>
      </dd>

      <template v-if="b.enabled">
        <dt>Names</dt>
        <dd>
          <template v-if="b.blocked">
            {{ formatCount(b.blocked) }} loaded
            <span v-if="b.memoryMb" class="text-neutral-500">· about {{ b.memoryMb }} MB</span>
          </template>
          <span v-else class="text-neutral-500">nothing merged yet</span>
        </dd>

        <dt>Lists</dt>
        <dd>
          {{ b.lists ?? 0 }} subscribed
          <span v-if="b.stale" class="badge badge-warn ml-2">stale</span>
        </dd>

        <dt>Last merge</dt>
        <dd>{{ when }}</dd>
      </template>

      <template v-if="b.enabled || b.allow || b.deny">
        <dt>Exceptions</dt>
        <dd>
          <span v-if="b.allow || b.deny">
            {{ b.allow ?? 0 }} allowed · {{ b.deny ?? 0 }} refused
          </span>
          <span v-else class="text-neutral-500">none</span>
        </dd>
      </template>
    </dl>
    <p v-if="!b.enabled" class="mt-2 text-sm text-neutral-500">
      Block lists are off. Turn them on under
      <RouterLink class="link" to="/services/dns#lists">Services, DNS, Block lists</RouterLink>.
    </p>
  </section>
</template>
