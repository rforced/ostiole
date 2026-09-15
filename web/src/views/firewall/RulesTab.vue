<script setup>
import { ArrowDown, ArrowUp, Plus } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { api } from '@/lib/api'
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

let poll = 0
async function refreshCounters() {
  try {
    counters.value = await api.counters()
  } catch {
    counters.value = {}
  }
}
onMounted(() => {
  refreshCounters()
  poll = window.setInterval(refreshCounters, 5000)
})
onBeforeUnmount(() => window.clearInterval(poll))

function describe(ep, ports) {
  let who = 'any'
  if (ep?.self) who = 'this firewall'
  else if (ep?.alias) who = `@${ep.alias}`
  else if (ep?.addresses?.length) who = ep.addresses.join(', ')
  let p = ''
  if (ep?.portAlias) p = ` : @${ep.portAlias}`
  else if (ep?.ports?.length) p = ` : ${ep.ports.join(', ')}`
  return ports ? who + p : who
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
      <span class="ml-auto text-xs text-neutral-500"
        >Rules run top to bottom; the first match wins. Anything unmatched is dropped.</span
      >
    </div>

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
        <tbody>
          <tr v-if="rules.length === 0">
            <td colspan="9" class="text-neutral-500">
              No rules in this zone. Everything entering it is dropped except the baseline and
              anti-lockout traffic.
            </td>
          </tr>
          <tr v-for="(r, i) in rules" :key="r.id" :class="{ 'opacity-50': !r.enabled }">
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
            <td class="font-mono text-xs">{{ r.protocol }}</td>
            <td class="font-mono text-xs">{{ describe(r.source, true) }}</td>
            <td class="font-mono text-xs">{{ describe(r.destination, true) }}</td>
            <td class="font-mono text-xs">{{ r.destZone ?? '' }}</td>
            <td>{{ r.description }}</td>
            <td class="text-right font-mono text-xs tabular-nums">
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
                confirm-label="Delete rule?"
                @confirm="config.removeRule(r.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <RuleDialog v-model:open="open" :rule="editing" :zone="zone" />
  </div>
</template>
