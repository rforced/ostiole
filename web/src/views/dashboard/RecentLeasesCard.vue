<script setup>
import { formatCount, formatDuration } from '@/lib/format'

defineProps({
  /** The leases handed out or renewed last, newest first. */
  leases: { type: Array, default: () => [] },
  /** How many leases there are altogether. */
  total: { type: Number, default: 0 },
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
  <section class="card" aria-labelledby="dash-leases">
    <h2 id="dash-leases" class="card-title">Newest leases</h2>
    <div class="-mx-4 overflow-x-auto">
      <table class="table">
        <thead>
          <tr>
            <th>Address</th>
            <th>Client</th>
            <th>Expires</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!leases.length" key="empty" class="row-static">
            <td colspan="3" class="text-neutral-500">No leases yet.</td>
          </tr>
          <tr v-for="l in leases" :key="l.ip + (l.mac || l.clientId)">
            <td class="font-mono text-code">{{ l.ip }}</td>
            <td>
              <div v-if="l.hostname" class="font-mono">{{ l.hostname }}</div>
              <div class="font-mono text-code" :class="l.hostname ? 'text-neutral-500' : ''">
                {{ l.mac || l.clientId || '—' }}
              </div>
            </td>
            <td class="whitespace-nowrap">{{ left(l) }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
    <p class="mt-3">
      <RouterLink to="/services/dhcp#leases" class="link">
        <template v-if="total"
          >All {{ formatCount(total) }} lease{{ total === 1 ? '' : 's' }}</template
        >
        <template v-else>All leases</template>
      </RouterLink>
    </p>
  </section>
</template>
