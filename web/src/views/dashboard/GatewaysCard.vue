<script setup>
defineProps({
  /** Gateway health from GET /overview. */
  gateways: { type: Array, default: () => [] },
})
</script>

<template>
  <section class="card" aria-labelledby="dash-gateways">
    <h2 id="dash-gateways" class="card-title">Gateways</h2>
    <div class="-mx-4 -mb-4 overflow-x-auto">
      <table class="table">
        <thead>
          <tr>
            <th>Gateway</th>
            <th>Via</th>
            <th>State</th>
            <th class="text-right">Latency</th>
            <th class="text-right">Loss</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!gateways.length" key="empty">
            <td colspan="5" class="text-neutral-500">No gateways configured.</td>
          </tr>
          <tr v-for="g in gateways" :key="g.name">
            <td>
              <span class="font-mono font-medium">{{ g.name }}</span>
              <span v-if="g.active" class="badge badge-ok ml-1">active</span>
            </td>
            <td class="font-mono text-code">
              {{ g.interface }}<span v-if="g.address"> · {{ g.address }}</span>
            </td>
            <td>
              <span v-if="g.unknown" class="badge">probing</span>
              <span v-else class="badge" :class="g.online ? 'badge-ok' : 'badge-warn'">
                {{ g.online ? 'up' : 'down' }}
              </span>
            </td>
            <td class="text-right font-mono text-code">{{ g.latencyMs.toFixed(1) }} ms</td>
            <td class="text-right font-mono text-code">{{ g.lossPercent.toFixed(0) }}%</td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
  </section>
</template>
