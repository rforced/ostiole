<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

/**
 * The edge defence: what a zone does about traffic that is too much of
 * itself rather than against the rules. Each defence is off until
 * somebody turns it on, because a limit that fires on a quiet network
 * arrives as a fault report from a user.
 *
 * The rate limits on individual rules are set on the rule, and listed
 * here so there is one place to see everything that is rationed.
 */

/** The periods a rate can be counted over, as the model spells them. */
const UNITS = [
  { value: 'second', label: 'per second' },
  { value: 'minute', label: 'per minute' },
  { value: 'hour', label: 'per hour' },
]

/** What each defence starts at when it is switched on. */
const DEFAULTS = {
  synFlood: { rate: 30, unit: 'second', burst: 60, perSource: true },
  icmpFlood: { rate: 10, unit: 'second', burst: 20, perSource: true },
  portScan: { rate: 20, unit: 'minute', hold: '10m' },
}

const config = useConfigStore()
const protection = computed(() => config.protection)

/** The zones on offer, and which are defended by default. */
const external = computed(() => config.zones.filter((z) => z.external).map((z) => z.name))
const chosen = computed(() => protection.value.zones ?? [])
const defended = computed(() => (chosen.value.length ? chosen.value : external.value))

function toggleDefence(which, on) {
  config.setDefence(which, on ? DEFAULTS[which] : null)
}

function toggleZone(name, on) {
  const next = new Set(chosen.value.length ? chosen.value : external.value)
  if (on) next.add(name)
  else next.delete(name)
  // Back to every external zone when the choice is the same as the
  // default, so the configuration does not carry a list that means
  // nothing.
  const names = config.zones.map((z) => z.name).filter((n) => next.has(n))
  const sameAsDefault =
    names.length === external.value.length && names.every((n) => external.value.includes(n))
  config.setProtectedZones(sameAsDefault ? [] : names)
}

/** A number field that keeps the model free of empty strings. */
function setNumber(which, field, value) {
  const n = Number.parseInt(value, 10)
  config.setDefence(which, { [field]: Number.isNaN(n) ? 0 : n })
}

const limited = computed(() => config.rules.filter((r) => r.limit))

/** How a limit reads in a sentence, the way the ruleset writes it. */
function rate(limit) {
  if (!limit) return ''
  const burst = limit.burst ? ` burst ${limit.burst}` : ''
  return `${limit.rate}/${limit.unit || 'second'}${burst}`
}
</script>

<template>
  <div v-if="config.draft" class="space-y-6">
    <section class="card space-y-4" aria-labelledby="prot-zones-title">
      <h2 id="prot-zones-title" class="card-title">Where</h2>
      <p class="max-w-3xl text-sm text-neutral-500">
        Defended zones. Without a choice this is every zone that faces the internet, which is where
        a flood comes from; a guest network is the other reasonable answer.
      </p>
      <ul class="flex flex-wrap gap-4">
        <li v-for="z in config.zones" :key="z.name" class="flex items-center gap-2">
          <input
            :id="`prot-zone-${z.name}`"
            type="checkbox"
            class="checkbox"
            :checked="defended.includes(z.name)"
            @change="toggleZone(z.name, $event.target.checked)"
          />
          <label :for="`prot-zone-${z.name}`">
            {{ z.name }}
            <span v-if="z.external" class="text-sm text-neutral-500">(faces the internet)</span>
          </label>
        </li>
      </ul>
      <p v-if="!defended.length" class="text-sm text-amber-700 dark:text-amber-300">
        Nothing is defended: no zone here faces the internet, so choose one.
      </p>
    </section>

    <section class="card space-y-4" aria-labelledby="prot-syn-title">
      <div class="flex items-start gap-3">
        <input
          id="prot-syn"
          type="checkbox"
          class="checkbox mt-1"
          :checked="!!protection.synFlood"
          @change="toggleDefence('synFlood', $event.target.checked)"
        />
        <div>
          <label id="prot-syn-title" for="prot-syn" class="card-title">Connection flood</label>
          <p class="max-w-3xl text-sm text-neutral-500">
            Holds each source to a number of new connections. Over it, the connections are dropped
            and the source keeps whatever it already has open.
          </p>
        </div>
      </div>
      <div v-if="protection.synFlood" class="flex flex-wrap gap-4">
        <FormField id="prot-syn-rate" label="Connections" hint="From one source.">
          <input
            id="prot-syn-rate"
            class="input w-28"
            type="number"
            min="1"
            :value="protection.synFlood.rate"
            @input="setNumber('synFlood', 'rate', $event.target.value)"
          />
        </FormField>
        <FormField id="prot-syn-unit" label="Period">
          <select
            id="prot-syn-unit"
            class="input w-40"
            :value="protection.synFlood.unit || 'second'"
            @change="config.setDefence('synFlood', { unit: $event.target.value })"
          >
            <option v-for="u in UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
          </select>
        </FormField>
        <FormField
          id="prot-syn-burst"
          label="Burst"
          hint="Allowed at once before the rate applies. One page load opens several."
        >
          <input
            id="prot-syn-burst"
            class="input w-28"
            type="number"
            min="0"
            :value="protection.synFlood.burst ?? 0"
            @input="setNumber('synFlood', 'burst', $event.target.value)"
          />
        </FormField>
      </div>
    </section>

    <section class="card space-y-4" aria-labelledby="prot-icmp-title">
      <div class="flex items-start gap-3">
        <input
          id="prot-icmp"
          type="checkbox"
          class="checkbox mt-1"
          :checked="!!protection.icmpFlood"
          @change="toggleDefence('icmpFlood', $event.target.checked)"
        />
        <div>
          <label id="prot-icmp-title" for="prot-icmp" class="card-title">Ping flood</label>
          <p class="max-w-3xl text-sm text-neutral-500">
            The same for echo requests, which open no connection and so are never counted as one.
            Replies to pings this router sent are not affected.
          </p>
        </div>
      </div>
      <div v-if="protection.icmpFlood" class="flex flex-wrap gap-4">
        <FormField id="prot-icmp-rate" label="Pings" hint="From one source.">
          <input
            id="prot-icmp-rate"
            class="input w-28"
            type="number"
            min="1"
            :value="protection.icmpFlood.rate"
            @input="setNumber('icmpFlood', 'rate', $event.target.value)"
          />
        </FormField>
        <FormField id="prot-icmp-unit" label="Period">
          <select
            id="prot-icmp-unit"
            class="input w-40"
            :value="protection.icmpFlood.unit || 'second'"
            @change="config.setDefence('icmpFlood', { unit: $event.target.value })"
          >
            <option v-for="u in UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
          </select>
        </FormField>
        <FormField id="prot-icmp-burst" label="Burst" hint="A traceroute sends a handful.">
          <input
            id="prot-icmp-burst"
            class="input w-28"
            type="number"
            min="0"
            :value="protection.icmpFlood.burst ?? 0"
            @input="setNumber('icmpFlood', 'burst', $event.target.value)"
          />
        </FormField>
      </div>
    </section>

    <section class="card space-y-4" aria-labelledby="prot-scan-title">
      <div class="flex items-start gap-3">
        <input
          id="prot-scan"
          type="checkbox"
          class="checkbox mt-1"
          :checked="!!protection.portScan"
          @change="toggleDefence('portScan', $event.target.checked)"
        />
        <div>
          <label id="prot-scan-title" for="prot-scan" class="card-title">Port scan</label>
          <p class="max-w-3xl text-sm text-neutral-500">
            A scan is a few packets to a great many closed ports, so it is counted where refused
            traffic ends up: past the last rule of the zone. A source that keeps arriving there is
            dropped outright for a while.
          </p>
        </div>
      </div>
      <div v-if="protection.portScan" class="flex flex-wrap gap-4">
        <FormField
          id="prot-scan-rate"
          label="Refusals"
          hint="From one source. A client with a stale bookmark is refused once or twice."
        >
          <input
            id="prot-scan-rate"
            class="input w-28"
            type="number"
            min="1"
            :value="protection.portScan.rate"
            @input="setNumber('portScan', 'rate', $event.target.value)"
          />
        </FormField>
        <FormField id="prot-scan-unit" label="Period">
          <select
            id="prot-scan-unit"
            class="input w-40"
            :value="protection.portScan.unit || 'minute'"
            @change="config.setDefence('portScan', { unit: $event.target.value })"
          >
            <option v-for="u in UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
          </select>
        </FormField>
        <FormField
          id="prot-scan-hold"
          label="Hold for"
          hint="A number and a unit: 30s, 10m, 1h. An address shared behind carrier NAT is held too."
        >
          <input
            id="prot-scan-hold"
            class="input w-28 font-mono"
            :value="protection.portScan.hold || '10m'"
            @change="config.setDefence('portScan', { hold: $event.target.value })"
          />
        </FormField>
      </div>
    </section>

    <section class="card space-y-3" aria-labelledby="prot-rules-title">
      <h2 id="prot-rules-title" class="card-title">Rules with a limit</h2>
      <p class="max-w-3xl text-sm text-neutral-500">
        A limit is set on the rule that admits the traffic, so this shows rather than edits. Over
        the limit, a packet falls through to whatever comes next, which on the last rule of a zone
        is the drop at the end of it.
      </p>
      <p v-if="!limited.length" class="text-sm text-neutral-500">
        No rule holds its traffic to a rate.
      </p>
      <div
        v-else
        class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800"
      >
        <table class="table">
          <thead>
            <tr>
              <th>Rule</th>
              <th>Zone</th>
              <th>Rate</th>
              <th>Counted</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in limited" :key="r.id">
              <td>
                <RouterLink class="link" :to="`/firewall/rules#${r.zone}`">
                  {{ r.description || r.id }}
                </RouterLink>
              </td>
              <td>{{ r.zone }}</td>
              <td class="font-mono text-code">{{ rate(r.limit) }}</td>
              <td>{{ r.limit.perSource ? 'per source' : 'everybody together' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>
