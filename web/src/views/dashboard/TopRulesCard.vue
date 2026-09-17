<script setup>
import { formatBytes, formatCount } from '@/lib/format'

defineProps({
  /** Busiest configured rules from GET /overview. */
  rules: { type: Array, default: () => [] },
  /** Packets that reached a default drop. */
  blocked: { type: Object, default: () => ({ packets: 0, bytes: 0 }) },
})
</script>

<template>
  <section class="card" aria-labelledby="dash-rules">
    <h2 id="dash-rules" class="card-title">Busiest rules</h2>
    <div class="-mx-4 overflow-x-auto">
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
            <td colspan="4" class="text-neutral-500">No rules with counters yet.</td>
          </tr>
          <tr v-for="r in rules" :key="r.id">
            <td>
              <div>{{ r.description || r.id }}</div>
              <div class="text-neutral-500">
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
    </div>
    <p class="mt-3 text-sm text-neutral-500">
      Blocked by the default policy: {{ formatCount(blocked?.packets ?? 0) }} packets ({{
        formatBytes(blocked?.bytes ?? 0)
      }}) ·
      <RouterLink to="/firewall/log" class="link">see the log</RouterLink>
    </p>
  </section>
</template>
