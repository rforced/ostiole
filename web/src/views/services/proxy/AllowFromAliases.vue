<script setup lang="ts">
import { computed, type PropType } from 'vue'

import { useConfigStore } from '@/stores/config'
import { smallAliases } from '@/views/services/proxy/allowFrom'

/**
 * The hosts aliases Allow from may name beside its prefixes: those with
 * their entries written in them. The proxy checks every range on every
 * request, so a fetched list goes in the access list instead.
 */
const model = defineModel({ type: Array as PropType<string[]>, default: () => [] })

const config = useConfigStore()
const aliases = computed(() => smallAliases(config.aliases))

function toggle(name: string, on: boolean) {
  const next = model.value.filter((n) => n !== name)
  if (on) next.push(name)
  model.value = next
}
</script>

<template>
  <div v-if="aliases.length" class="flex flex-wrap gap-4">
    <label v-for="a in aliases" :key="a.name" class="flex items-center gap-2">
      <input
        type="checkbox"
        class="checkbox"
        :checked="model.includes(a.name)"
        @change="toggle(a.name, ($event.target as HTMLInputElement).checked)"
      />
      <span class="font-mono">{{ a.name }}</span>
    </label>
  </div>
</template>
