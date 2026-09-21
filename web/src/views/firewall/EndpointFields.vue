<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

/**
 * Source or destination editor: any / addresses / alias (/ self for
 * destinations), plus ports when the protocol carries them.
 */
const props = defineProps({
  side: { type: String, required: true }, // 'source' | 'destination'
  portsAllowed: { type: Boolean, default: false },
})
const model = defineModel({ type: Object, required: true })

const config = useConfigStore()
// geoip and asn aliases resolve to address sets too, so they belong here
// beside the hand-written ones; only port aliases are a different kind of
// thing.
const hostAliases = computed(() => config.aliases.filter((a) => a.type !== 'ports'))
const portAliases = computed(() => config.aliases.filter((a) => a.type === 'ports'))
const id = (name) => `${props.side}-${name}`
const label = computed(() => (props.side === 'source' ? 'Source' : 'Destination'))
</script>

<template>
  <fieldset class="space-y-3 rounded-md border border-line p-3">
    <legend class="group-title px-1">{{ label }}</legend>
    <FormField :id="id('mode')" label="Match">
      <select :id="id('mode')" v-model="model.mode" class="input">
        <option value="any">Any</option>
        <option value="addresses">Addresses or networks</option>
        <option value="alias" :disabled="hostAliases.length === 0">Alias</option>
        <option v-if="side === 'destination'" value="self">This firewall</option>
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
    <label v-if="model.mode !== 'any'" class="flex items-start gap-2 text-sm">
      <input
        :id="id('not')"
        v-model="model.notAddresses"
        type="checkbox"
        class="mt-0.5 size-4 rounded border-line-2"
      />
      <span>
        <span class="font-medium">Invert</span>: match everything <em>except</em> this, IPv6
        included when the list is IPv4 only.
      </span>
    </label>
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
        hint="Comma or space separated, e.g. 80, 443, 8000-8100."
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
      <label v-if="model.portMode !== 'any'" class="flex items-start gap-2 text-sm">
        <input
          :id="id('notports')"
          v-model="model.notPorts"
          type="checkbox"
          class="mt-0.5 size-4 rounded border-line-2"
        />
        <span>
          <span class="font-medium">Invert ports</span>: match every port <em>except</em> these.
        </span>
      </label>
    </template>
  </fieldset>
</template>
