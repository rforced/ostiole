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
const hostAliases = computed(() => config.aliases.filter((a) => a.type === 'hosts'))
const portAliases = computed(() => config.aliases.filter((a) => a.type === 'ports'))
const id = (name) => `${props.side}-${name}`
const label = computed(() => (props.side === 'source' ? 'Source' : 'Destination'))
</script>

<template>
  <fieldset class="space-y-3 rounded-md border border-neutral-200 p-3 dark:border-neutral-800">
    <legend class="px-1 text-sm font-medium">{{ label }}</legend>
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
      hint="One per line: IPs or CIDR networks, IPv4 and IPv6."
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
        <option v-for="a in hostAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
      </select>
    </FormField>
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
    </template>
  </fieldset>
</template>
