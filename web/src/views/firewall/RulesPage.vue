<script setup>
import { ArrowDown, ArrowUp, Plus } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import RuleDialog from '@/views/firewall/RuleDialog.vue'

const config = useConfigStore()
const zone = ref(config.zones[0]?.name ?? '')
const editing = ref(null)
const open = ref(false)
const counters = ref({})

watch(
  () => config.zones.map((z) => z.name),
  (names) => {
    if (!names.includes(zone.value)) zone.value = names[0] ?? ''
  },
)

const rules = computed(() => config.rulesForZone(zone.value))

/**
 * Which interfaces the selected zone covers. Worth showing: a VLAN assigned
 * to an existing zone has no tab of its own here, and without this the only
 * clue is that the zone list is shorter than the interface list.
 */
const members = computed(() =>
  config.interfaces.filter((i) => i.zone === zone.value).map((i) => i.name),
)

// Counters are decoration; a failed read leaves the column blank.
useAsync(
  async () => {
    counters.value = await api.counters()
  },
  { interval: 5000, immediate: true },
)

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

function add() {
  editing.value = null
  open.value = true
}

function edit(rule) {
  editing.value = rule
  open.value = true
}

function toggle(rule) {
  config.upsertRule({ ...rule, enabled: !rule.enabled })
}
</script>

<template>
  <div class="space-y-3">
    <div class="flex flex-wrap items-center gap-2">
      <div
        class="flex gap-1 rounded-md border border-neutral-200 p-0.5 dark:border-neutral-800"
        role="group"
        aria-label="Zone"
      >
        <button
          v-for="z in config.zones"
          :key="z.name"
          type="button"
          class="rounded px-2.5 py-1 font-mono text-sm"
          :class="
            z.name === zone
              ? 'bg-neutral-200 text-neutral-900 dark:bg-neutral-800 dark:text-neutral-100'
              : 'text-neutral-500 hover:text-neutral-900 dark:hover:text-neutral-100'
          "
          :aria-pressed="z.name === zone"
          @click="zone = z.name"
        >
          {{ z.name }}
        </button>
      </div>
      <button type="button" class="btn-secondary" :disabled="!zone" @click="add">
        <Plus class="mr-1 size-4" aria-hidden="true" /> Add rule
      </button>
      <span class="ml-auto text-sm text-neutral-500"
        >First match wins, top to bottom. Unmatched traffic is dropped.</span
      >
    </div>

    <p v-if="zone" class="text-sm text-neutral-500">
      <template v-if="members.length">
        Zone <span class="font-mono">{{ zone }}</span> covers
        <span class="font-mono">{{ members.join(', ') }}</span
        >. To give one of them rules of its own,
        <RouterLink to="/interfaces" class="underline">move it to its own zone</RouterLink>.
      </template>
      <template v-else>
        No interface is in zone <span class="font-mono">{{ zone }}</span
        >, so these rules match nothing.
        <RouterLink to="/interfaces" class="underline">Assign one</RouterLink>.
      </template>
    </p>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th class="w-8"></th>
            <th>Action</th>
            <th>Protocol</th>
            <th>Source</th>
            <th>Destination</th>
            <th>Via</th>
            <th>Description</th>
            <th class="text-right">Packets</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="rules.length === 0" key="empty">
            <td colspan="9" class="text-neutral-500">
              No rules in this zone. Everything entering it is dropped except the baseline and
              anti-lockout traffic.
            </td>
          </tr>
          <tr
            v-for="(r, i) in rules"
            :key="r.id"
            :class="{ 'opacity-50': !r.enabled, 'row-changed': config.isChanged('rules', r.id) }"
          >
            <td>
              <input
                type="checkbox"
                class="size-4 rounded border-neutral-300"
                :checked="r.enabled"
                :aria-label="`Enable ${r.id}`"
                @change="toggle(r)"
              />
            </td>
            <td>
              <span
                class="badge"
                :class="{ 'badge-ok': r.action === 'accept', 'badge-warn': r.action !== 'accept' }"
                >{{ r.action }}</span
              >
              <span v-if="r.log" class="badge ml-1">log</span>
              <span v-if="r.schedule" class="badge ml-1">{{ r.schedule }}</span>
            </td>
            <td class="font-mono text-code">{{ r.protocol }}</td>
            <td class="font-mono text-code">{{ describe(r.source, true) }}</td>
            <td class="font-mono text-code">{{ describe(r.destination, true) }}</td>
            <td class="font-mono text-code">
              <span v-if="r.gateway" class="badge" :title="`Routed through ${r.gateway}`"
                >→ {{ r.gateway }}</span
              >
              <template v-else>{{ r.destZone ?? '' }}</template>
            </td>
            <td>{{ r.description }}</td>
            <td class="text-right font-mono text-code tabular-nums">
              {{ counters[r.id]?.packets ?? '' }}
            </td>
            <td class="text-right whitespace-nowrap">
              <button
                type="button"
                class="icon-btn"
                :disabled="i === 0"
                :aria-label="`Move ${r.id} up`"
                @click="config.moveRule(r.id, -1)"
              >
                <ArrowUp class="size-4" />
              </button>
              <button
                type="button"
                class="icon-btn"
                :disabled="i === rules.length - 1"
                :aria-label="`Move ${r.id} down`"
                @click="config.moveRule(r.id, 1)"
              >
                <ArrowDown class="size-4" />
              </button>
              <button type="button" class="link ml-2" @click="edit(r)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete rule ${r.id}?`"
                :description="r.description"
                @confirm="config.removeRule(r.id)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </div>

    <RuleDialog v-model:open="open" :rule="editing" :zone="zone" />
  </div>
</template>
