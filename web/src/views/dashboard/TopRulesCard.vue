<script setup>
import SectionCard from '@/components/SectionCard.vue'
import { formatBytes, formatCount } from '@/lib/format'

defineProps({
  /** Busiest configured rules from GET /overview. */
  rules: { type: Array, default: () => [] },
  /** Packets that reached a default drop. */
  blocked: { type: Object, default: () => ({ packets: 0, bytes: 0 }) },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
})
</script>

<template>
  <SectionCard title="Busiest rules" flush>
    <table class="table">
      <thead>
        <tr>
          <th>Rule</th>
          <th>Zone</th>
          <th class="text-right">Packets</th>
          <th class="text-right">Bytes</th>
        </tr>
      </thead>
      <TransitionGroup name="row" tag="tbody">
        <tr v-if="!rules.length" key="empty" class="row-static">
          <td colspan="4" class="text-ink-muted">
            {{ loaded ? 'No rules with counters yet.' : 'Reading…' }}
          </td>
        </tr>
        <tr v-for="r in rules" :key="r.id">
          <td>
            <div>{{ r.description || r.id }}</div>
            <div class="text-ink-muted">
              <span class="badge" :class="r.action === 'accept' ? 'badge-ok' : 'badge-warn'">{{
                r.action
              }}</span>
              <span v-if="!r.enabled" class="ml-1">disabled</span>
            </div>
          </td>
          <td class="font-mono text-code">{{ r.zone }}</td>
          <td class="text-right font-mono text-code">{{ formatCount(r.packets) }}</td>
          <td class="text-right font-mono text-code">{{ formatBytes(r.bytes) }}</td>
        </tr>
      </TransitionGroup>
    </table>
    <p class="card-strip border-t border-line text-ink-muted">
      Blocked by the default policy: {{ formatCount(blocked?.packets ?? 0) }} packets ({{
        formatBytes(blocked?.bytes ?? 0)
      }}) ·
      <RouterLink to="/firewall/log" class="link">see the log</RouterLink>
    </p>
  </SectionCard>
</template>
