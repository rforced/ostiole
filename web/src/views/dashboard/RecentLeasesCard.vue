<script setup>
import SectionCard from '@/components/SectionCard.vue'
import { formatCount, formatDuration } from '@/lib/format'

defineProps({
  /** The leases handed out or renewed last, newest first. */
  leases: { type: Array, default: () => [] },
  /** How many leases there are altogether. */
  total: { type: Number, default: 0 },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many rows to hold room for until then. */
  placeholders: { type: Number, default: 1 },
})

/** How long a lease has left, as the dashboard is read. */
function left(l) {
  if (l.static) return 'static'
  const seconds = (new Date(l.expires).getTime() - Date.now()) / 1000
  if (!Number.isFinite(seconds) || seconds <= 0) return 'expired'
  return `${formatDuration(seconds)} left`
}
</script>

<template>
  <SectionCard title="Newest leases" flush :aria-busy="loaded ? undefined : 'true'">
    <table class="table">
      <thead>
        <tr>
          <th>Address</th>
          <th>Client</th>
          <th>Expires</th>
        </tr>
      </thead>
      <tbody v-if="!loaded">
        <tr v-if="!placeholders" data-reading>
          <td colspan="3">
            <span class="sr-only">Reading…</span><span class="skeleton w-28"></span>
          </td>
        </tr>
        <tr v-for="n in placeholders" :key="n" data-reading>
          <td>
            <span v-if="n === 1" class="sr-only">Reading…</span>
            <span class="skeleton w-24"></span>
          </td>
          <td>
            <div><span class="skeleton w-20"></span></div>
            <div><span class="skeleton w-32"></span></div>
          </td>
          <td><span class="skeleton w-16"></span></td>
        </tr>
      </tbody>
      <tbody v-else>
        <tr v-if="!leases.length">
          <td colspan="3" class="text-ink-muted">No leases yet.</td>
        </tr>
        <tr v-for="l in leases" :key="l.ip + (l.mac || l.clientId)">
          <td class="font-mono text-code">{{ l.ip }}</td>
          <td>
            <div v-if="l.hostname" class="font-mono">{{ l.hostname }}</div>
            <div class="font-mono text-code" :class="l.hostname ? 'text-ink-muted' : ''">
              {{ l.mac || l.clientId || '—' }}
            </div>
          </td>
          <td class="whitespace-nowrap">{{ left(l) }}</td>
        </tr>
      </tbody>
    </table>
    <p class="card-strip border-t border-line">
      <RouterLink to="/services/dhcp#leases" class="link">
        <template v-if="total"
          >All {{ formatCount(total) }} lease{{ total === 1 ? '' : 's' }}</template
        >
        <template v-else>All leases</template>
      </RouterLink>
    </p>
  </SectionCard>
</template>
