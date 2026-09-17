<script setup>
import { computed } from 'vue'

import { useConfigStore } from '@/stores/config'
import { tierBadge, tierLabel } from '@/views/firewall/shaping/tiers'

const config = useConfigStore()

/**
 * A priority is set where the traffic is admitted, so this page shows
 * rather than edits: it is the one place to see everything that has been
 * classified, with a way back to the rule that did it.
 */
function describe(ep, ports) {
  let who = 'any'
  if (ep?.self) who = 'this firewall'
  else if (ep?.alias) who = `@${ep.alias}`
  else if (ep?.addresses?.length) who = ep.addresses.join(', ')
  if (ep?.notAddresses) who = `not ${who}`
  let p = ''
  if (ep?.portAlias) p = `@${ep.portAlias}`
  else if (ep?.ports?.length) p = ep.ports.join(', ')
  if (p && ep?.notPorts) p = `not ${p}`
  return ports && p ? `${who} : ${p}` : who
}

const entries = computed(() => [
  ...config.shapedRules.map((r) => ({
    key: `rule:${r.id}`,
    id: r.id,
    kind: 'Rule',
    to: '/firewall/rules',
    zone: r.destZone ? `${r.zone} → ${r.destZone}` : r.zone,
    match: `${r.protocol} ${describe(r.source, true)} → ${describe(r.destination, true)}`,
    priority: r.priority,
    enabled: r.enabled,
    description: r.description,
  })),
  ...config.shapedForwards.map((pf) => ({
    key: `forward:${pf.id}`,
    id: pf.id,
    kind: 'Port forward',
    to: '/firewall/nat',
    zone: pf.zone,
    match: `${pf.protocol} ${(pf.ports ?? []).join(', ')} → ${pf.target}${
      pf.targetPort ? `:${pf.targetPort}` : ''
    }`,
    priority: pf.priority,
    enabled: pf.enabled,
    description: pf.description,
  })),
])
</script>

<template>
  <div class="space-y-3">
    <p class="text-sm text-neutral-500">
      A priority is set on the rule or port forward that admits the traffic, and follows the
      connection both ways. Everything not listed here is Normal.
    </p>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Priority</th>
            <th>Where</th>
            <th>Zone</th>
            <th>Match</th>
            <th>Description</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!entries.length" key="empty" class="row-static">
            <td colspan="5" class="text-neutral-500">
              No rule sets a priority. All traffic is Normal.
            </td>
          </tr>
          <tr v-for="e in entries" :key="e.key" :class="{ 'opacity-50': !e.enabled }">
            <td>
              <span :class="tierBadge(e.priority)">{{ tierLabel(e.priority) }}</span>
            </td>
            <td>
              <RouterLink :to="e.to" class="link font-mono">{{ e.id }}</RouterLink>
              <span class="ml-2 text-xs text-neutral-500">{{ e.kind }}</span>
            </td>
            <td class="font-mono text-code">{{ e.zone }}</td>
            <td class="font-mono text-code">{{ e.match }}</td>
            <td>{{ e.description }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
  </div>
</template>
