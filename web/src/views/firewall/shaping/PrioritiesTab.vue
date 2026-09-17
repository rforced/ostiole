<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { formatCount } from '@/lib/format'
import { useConfigStore } from '@/stores/config'
import BusyHostsDialog from '@/views/firewall/shaping/BusyHostsDialog.vue'
import { tierBadge, tierLabel } from '@/views/firewall/shaping/tiers'

const config = useConfigStore()
const editing = ref(null)
const open = ref(false)

function addBusy() {
  editing.value = null
  open.value = true
}

function editBusy(zone) {
  editing.value = zone
  open.value = true
}

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
    to: `/firewall/rules#${r.zone}`,
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
  <div class="space-y-6">
    <section class="space-y-3" aria-labelledby="busy-title">
      <div class="flex flex-wrap items-center gap-3">
        <h2 id="busy-title" class="section-title">Busy hosts</h2>
        <button type="button" class="btn-secondary" @click="addBusy">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Hold back busy hosts
        </button>
      </div>
      <p class="text-sm text-neutral-500">
        A device with a lot of connections open at once is usually sharing files, whatever port it
        is doing it on and however well encrypted. Counting them catches that where a list of ports
        no longer can.
      </p>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Zone</th>
              <th>Past</th>
              <th>Priority</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!config.busyZones.length" key="empty" class="row-static">
              <td colspan="4" class="text-neutral-500">
                No zone holds a busy device back. Every device gets an equal share whatever it is
                doing.
              </td>
            </tr>
            <tr
              v-for="z in config.busyZones"
              :key="z.name"
              :class="{ 'row-changed': config.isChanged('zones', z.name) }"
            >
              <td class="font-mono">{{ z.name }}</td>
              <td class="font-mono text-code tabular-nums">
                {{ formatCount(z.busy.connections) }} connections
              </td>
              <td>
                <span :class="tierBadge(z.busy.priority)">{{ tierLabel(z.busy.priority) }}</span>
              </td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editBusy(z)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Remove"
                  :question="`Stop holding back busy devices on ${z.name}?`"
                  description="Every device on the zone goes back to an equal share whatever it is doing."
                  @confirm="config.clearBusy(z.name)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>

    <section class="space-y-3" aria-labelledby="rules-title">
      <h2 id="rules-title" class="section-title">Rules and port forwards</h2>
      <p class="text-sm text-neutral-500">
        A priority is set on the rule or port forward that admits the traffic, and follows the
        connection both ways. Anything not listed here keeps whatever marking its device asked for.
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
                No rule sets a priority, so every flow is sorted by what its device asks for.
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
    </section>

    <BusyHostsDialog v-model:open="open" :zone="editing" />
  </div>
</template>
