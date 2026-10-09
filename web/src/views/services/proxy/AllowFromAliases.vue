<script setup>
import { computed } from 'vue'

import { useConfigStore } from '@/stores/config'

/**
 * The hosts aliases Allow from may name beside its prefixes: those with
 * their entries written in them. The proxy checks every range on every
 * request, so a fetched list goes in the access list instead.
 */
const model = defineModel({ type: Array, default: () => [] })

const config = useConfigStore()
const aliases = computed(() => smallAliases(config.aliases))

function toggle(name, on) {
  const next = model.value.filter((n) => n !== name)
  if (on) next.push(name)
  model.value = next
}
</script>

<script>
import { fetches } from '@/lib/aliases'

/**
 * @param {object[]} aliases
 * @returns {object[]} the hosts aliases that are not fetched
 */
export function smallAliases(aliases) {
  return aliases.filter((a) => a.type === 'hosts' && !fetches(a))
}

/**
 * Splits Allow from into the prefixes typed and the small aliases named.
 * Any other name stays with the prefixes, where the check will say why it
 * does not belong.
 * @param {string[]} [from]
 * @param {object[]} aliases
 */
export function splitAllowFrom(from = [], aliases = []) {
  const small = new Set(smallAliases(aliases).map((a) => a.name))
  return {
    prefixes: from.filter((f) => !small.has(f)),
    aliases: from.filter((f) => small.has(f)),
  }
}
</script>

<template>
  <div v-if="aliases.length" class="flex flex-wrap gap-4">
    <label v-for="a in aliases" :key="a.name" class="flex items-center gap-2">
      <input
        type="checkbox"
        class="checkbox"
        :checked="model.includes(a.name)"
        @change="toggle(a.name, $event.target.checked)"
      />
      <span class="font-mono">{{ a.name }}</span>
    </label>
  </div>
</template>
