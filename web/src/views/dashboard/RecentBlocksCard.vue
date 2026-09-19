<script setup>
defineProps({
  /** The newest refused packets from GET /overview, newest first. */
  blocks: { type: Array, default: () => [] },
})

function endpoint(addr, port) {
  if (!addr) return '—'
  return port ? `${addr}:${port}` : addr
}

/** What refused the packet: a rule by id, or the zone or default policy. */
function by(e) {
  if (e.kind === 'rule') return e.ruleId
  if (e.kind === 'zone-drop') return `${e.zone} default`
  return 'default drop'
}
</script>

<template>
  <section class="card" aria-labelledby="dash-blocks">
    <h2 id="dash-blocks" class="card-title">Recent blocks</h2>
    <div class="-mx-4 overflow-x-auto">
      <table class="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Source</th>
            <th>Destination</th>
            <th>By</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!blocks.length" key="empty" class="row-static">
            <td colspan="4" class="text-neutral-500">Nothing refused lately.</td>
          </tr>
          <tr v-for="e in blocks" :key="`${e.time}-${e.src}-${e.srcPort}-${e.dst}-${e.dstPort}`">
            <td class="text-xs whitespace-nowrap text-neutral-500">
              {{ new Date(e.time).toLocaleTimeString() }}
            </td>
            <td class="font-mono text-code">{{ endpoint(e.src, e.srcPort) }}</td>
            <td class="font-mono text-code">{{ endpoint(e.dst, e.dstPort) }}</td>
            <td>
              <div class="font-mono text-code">{{ by(e) }}</div>
              <div class="font-mono text-code text-neutral-500">
                {{ e.proto }}<span v-if="e.in"> · {{ e.in }}</span>
              </div>
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
    <p class="mt-3">
      <RouterLink to="/firewall/log" class="link">Full log</RouterLink>
    </p>
  </section>
</template>
