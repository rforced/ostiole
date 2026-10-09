<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useConfigStore } from '@/stores/config'

/**
 * Source or destination editor: any / addresses / alias / WireGuard peer
 * (/ self for destinations), plus ports when the protocol carries them.
 */
const props = defineProps({
  side: { type: String, required: true }, // 'source' | 'destination'
  portsAllowed: { type: Boolean, default: false },
  /** The zone the rule looks at: a source peer has to arrive there. */
  zone: { type: String, default: '' },
})
const model = defineModel({ type: Object, required: true })

const config = useConfigStore()
// geoip and asn aliases resolve to address sets too, so they belong here
// beside the hand-written ones; only port aliases are a different kind of
// thing.
const hostAliases = computed(() => config.aliases.filter((a) => a.type !== 'ports'))
const portAliases = computed(() => config.aliases.filter((a) => a.type === 'ports'))
/**
 * The peers a rule can name, as "laptop on wg0". One that takes a default
 * route stands for the whole internet, and a source has to arrive in the
 * rule's zone.
 */
const peers = computed(() =>
  config.tunnels
    .filter((t) => props.side !== 'source' || !props.zone || t.zone === props.zone)
    .flatMap((t) =>
      (t.wireguard.peers ?? [])
        .filter((p) => !(p.allowedIps ?? []).some((a) => /\/0$/.test(a.trim())))
        .map((p) => ({ ref: `${t.name}/${p.name}`, label: `${p.name} on ${t.name}` })),
    ),
)
const id = (name) => `${props.side}-${name}`
const label = computed(() => (props.side === 'source' ? 'Source' : 'Destination'))
</script>

<template>
  <fieldset class="field-group">
    <legend>{{ label }}</legend>
    <FormField :id="id('mode')" label="Match">
      <select :id="id('mode')" v-model="model.mode" class="input">
        <option value="any">Any</option>
        <option value="addresses">Addresses or networks</option>
        <option value="alias" :disabled="hostAliases.length === 0">Alias</option>
        <option value="peer" :disabled="peers.length === 0">WireGuard peer</option>
        <option v-if="side === 'destination'" value="self">This router</option>
      </select>
    </FormField>
    <FormField
      v-if="model.mode === 'addresses'"
      :id="id('addresses')"
      label="Addresses"
      hint="One address or CIDR network per line, IPv4 or IPv6."
    >
      <textarea
        :id="id('addresses')"
        v-model="model.addresses"
        class="input h-24 font-mono"
        spellcheck="false"
      ></textarea>
    </FormField>
    <FormField v-if="model.mode === 'alias'" :id="id('alias')" label="Alias">
      <select :id="id('alias')" v-model="model.alias" class="input" required>
        <option v-for="a in hostAliases" :key="a.name" :value="a.name">
          {{ a.name }}<template v-if="a.type !== 'hosts'"> ({{ a.type }})</template>
        </option>
      </select>
    </FormField>
    <FormField v-if="model.mode === 'peer'" :id="id('peer')" label="Peer">
      <select :id="id('peer')" v-model="model.peer" class="input" required>
        <option v-for="p in peers" :key="p.ref" :value="p.ref">{{ p.label }}</option>
      </select>
    </FormField>
    <ToggleRow
      v-if="model.mode !== 'any'"
      :id="id('not')"
      v-model="model.notAddresses"
      label="Invert"
    >
      <template #hint>
        Match everything <em>except</em> this, IPv6 included when the list is IPv4 only.
      </template>
    </ToggleRow>
    <template v-if="portsAllowed">
      <FormField :id="id('portmode')" label="Ports">
        <select :id="id('portmode')" v-model="model.portMode" class="input">
          <option value="any">Any</option>
          <option value="ports">Ports or ranges</option>
          <option value="alias" :disabled="portAliases.length === 0">Port alias</option>
        </select>
      </FormField>
      <FormField
        v-if="model.portMode === 'ports'"
        :id="id('ports')"
        label="Port list"
        hint="Comma or space separated: 80, 443, 8000-8100."
      >
        <input
          :id="id('ports')"
          v-model="model.ports"
          class="input font-mono"
          spellcheck="false"
          required
        />
      </FormField>
      <FormField v-if="model.portMode === 'alias'" :id="id('portalias')" label="Port alias">
        <select :id="id('portalias')" v-model="model.portAlias" class="input" required>
          <option v-for="a in portAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
        </select>
      </FormField>
      <ToggleRow
        v-if="model.portMode !== 'any'"
        :id="id('notports')"
        v-model="model.notPorts"
        label="Invert ports"
      >
        <template #hint>Match every port <em>except</em> these.</template>
      </ToggleRow>
    </template>
  </fieldset>
</template>
