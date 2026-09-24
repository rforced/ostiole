<script setup>
import { computed } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useAuthStore } from '@/stores/auth'
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

const auth = useAuthStore()
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

/**
 * The ceiling on tracked connections. Empty is not zero: it leaves the
 * kernel's own limit, which it sizes from installed memory and which is
 * the right answer until a router actually runs out.
 */
const conntrackMax = computed({
  get: () => config.draft?.system?.conntrackMax || '',
  set: (v) => {
    const system = config.draft?.system
    if (!system) return
    if (Number.isFinite(v) && v > 0) system.conntrackMax = v
    else delete system.conntrackMax
  },
})

/** How a limit reads in a sentence, the way the ruleset writes it. */
function rate(limit) {
  if (!limit) return ''
  const burst = limit.burst ? ` burst ${limit.burst}` : ''
  return `${limit.rate}/${limit.unit || 'second'}${burst}`
}
</script>

<template>
  <div v-if="config.draft" class="space-y-5">
    <SectionCard
      title="Where"
      intro="Defended zones. None chosen means every zone that faces the internet."
      :locked="auth.readOnly"
    >
      <div class="space-y-4">
        <ul class="flex flex-wrap gap-4">
          <li v-for="z in config.zones" :key="z.name" class="flex items-center gap-2">
            <input
              :id="`prot-zone-${z.name}`"
              type="checkbox"
              class="size-4 rounded"
              :checked="defended.includes(z.name)"
              @change="toggleZone(z.name, $event.target.checked)"
            />
            <label :for="`prot-zone-${z.name}`">
              {{ z.name }}
              <span v-if="z.external" class="text-ink-muted">(faces the internet)</span>
            </label>
          </li>
        </ul>
        <AppNotice v-if="!defended.length">
          Nothing is defended: no zone here faces the internet, so choose one.
        </AppNotice>
      </div>
    </SectionCard>

    <SectionCard
      title="Connection flood"
      intro="Holds each source to a number of new connections. Over it, the connections are dropped
        and the source keeps whatever it already has open."
      :locked="auth.readOnly"
    >
      <template #actions>
        <ToggleRow
          id="prot-syn"
          :model-value="!!protection.synFlood"
          variant="switch"
          label="Enabled"
          aria-label="Connection flood enabled"
          :disabled="auth.readOnly"
          @update:model-value="toggleDefence('synFlood', $event)"
        />
      </template>
      <div v-if="protection.synFlood" class="flex flex-wrap gap-4">
        <FormField id="prot-syn-rate" label="Connections" hint="From one source.">
          <input
            id="prot-syn-rate"
            class="input w-28 max-sm:w-full"
            type="number"
            min="1"
            :value="protection.synFlood.rate"
            @input="setNumber('synFlood', 'rate', $event.target.value)"
          />
        </FormField>
        <FormField id="prot-syn-unit" label="Period">
          <select
            id="prot-syn-unit"
            class="input w-40 max-sm:w-full"
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
            class="input w-28 max-sm:w-full"
            type="number"
            min="0"
            :value="protection.synFlood.burst ?? 0"
            @input="setNumber('synFlood', 'burst', $event.target.value)"
          />
        </FormField>
      </div>
    </SectionCard>

    <SectionCard
      title="Ping flood"
      intro="The same for echo requests, which open no connection and so are never counted as one.
        Replies to pings this router sent are not affected."
      :locked="auth.readOnly"
    >
      <template #actions>
        <ToggleRow
          id="prot-icmp"
          :model-value="!!protection.icmpFlood"
          variant="switch"
          label="Enabled"
          aria-label="Ping flood enabled"
          :disabled="auth.readOnly"
          @update:model-value="toggleDefence('icmpFlood', $event)"
        />
      </template>
      <div v-if="protection.icmpFlood" class="flex flex-wrap gap-4">
        <FormField id="prot-icmp-rate" label="Pings" hint="From one source.">
          <input
            id="prot-icmp-rate"
            class="input w-28 max-sm:w-full"
            type="number"
            min="1"
            :value="protection.icmpFlood.rate"
            @input="setNumber('icmpFlood', 'rate', $event.target.value)"
          />
        </FormField>
        <FormField id="prot-icmp-unit" label="Period">
          <select
            id="prot-icmp-unit"
            class="input w-40 max-sm:w-full"
            :value="protection.icmpFlood.unit || 'second'"
            @change="config.setDefence('icmpFlood', { unit: $event.target.value })"
          >
            <option v-for="u in UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
          </select>
        </FormField>
        <FormField id="prot-icmp-burst" label="Burst" hint="A traceroute sends a handful.">
          <input
            id="prot-icmp-burst"
            class="input w-28 max-sm:w-full"
            type="number"
            min="0"
            :value="protection.icmpFlood.burst ?? 0"
            @input="setNumber('icmpFlood', 'burst', $event.target.value)"
          />
        </FormField>
      </div>
    </SectionCard>

    <SectionCard
      title="Port scan"
      intro="A scan is a few packets to a great many closed ports, so it is counted where refused
        traffic ends up: past the last rule of the zone. A source that keeps arriving there is
        dropped outright for a while."
      :locked="auth.readOnly"
    >
      <template #actions>
        <ToggleRow
          id="prot-scan"
          :model-value="!!protection.portScan"
          variant="switch"
          label="Enabled"
          aria-label="Port scan enabled"
          :disabled="auth.readOnly"
          @update:model-value="toggleDefence('portScan', $event)"
        />
      </template>
      <div v-if="protection.portScan" class="flex flex-wrap gap-4">
        <FormField
          id="prot-scan-rate"
          label="Refusals"
          hint="From one source. A client with a stale bookmark is refused once or twice."
        >
          <input
            id="prot-scan-rate"
            class="input w-28 max-sm:w-full"
            type="number"
            min="1"
            :value="protection.portScan.rate"
            @input="setNumber('portScan', 'rate', $event.target.value)"
          />
        </FormField>
        <FormField id="prot-scan-unit" label="Period">
          <select
            id="prot-scan-unit"
            class="input w-40 max-sm:w-full"
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
            class="input w-28 font-mono max-sm:w-full"
            :value="protection.portScan.hold || '10m'"
            @change="config.setDefence('portScan', { hold: $event.target.value })"
          />
        </FormField>
      </div>
    </SectionCard>

    <SectionCard
      title="Rules with a limit"
      :count="limited.length"
      intro="A limit is set on the rule that admits the traffic, so this shows rather than edits.
        Over the limit, a packet falls through to whatever comes next, which on the last rule of a
        zone is the drop at the end of it."
      flush
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
          <tr v-if="!limited.length">
            <td colspan="4" class="text-ink-muted">No rule holds its traffic to a rate.</td>
          </tr>
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
    </SectionCard>

    <SectionCard title="Connection table" :locked="auth.readOnly">
      <template #intro>
        Every connection through the router takes one entry, and the kernel drops packets once the
        table is full. Empty keeps the kernel's own limit, sized from installed memory. Raise it
        when <RouterLink class="link" to="/diagnostics/connections">the table</RouterLink> runs
        close to full.
      </template>
      <FormField
        id="prot-conntrack"
        label="Connections tracked at once"
        hint="About 350 bytes of memory each."
      >
        <input
          id="prot-conntrack"
          v-model.number="conntrackMax"
          type="number"
          min="16384"
          max="4194304"
          placeholder="sized from memory"
          class="input w-48 max-sm:w-full"
        />
      </FormField>
    </SectionCard>
  </div>
</template>
