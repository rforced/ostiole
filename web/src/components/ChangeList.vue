<script setup>
import { computed } from 'vue'

const props = defineProps({
  changes: { type: Array, default: () => [] },
  /** Shown when there is nothing to report. */
  emptyLabel: { type: String, default: 'No differences.' },
  /** Longer lists are cut off until the reader asks for the rest. */
  limit: { type: Number, default: 0 },
})

const shown = computed(() =>
  props.limit > 0 ? props.changes.slice(0, props.limit) : props.changes,
)
const hidden = computed(() => props.changes.length - shown.value.length)

const marks = { added: '+', removed: '−', changed: '~' }
const tones = {
  added: 'text-emerald-700 dark:text-emerald-400',
  removed: 'text-red-700 dark:text-red-400',
  changed: 'text-amber-700 dark:text-amber-400',
}

/** Renders a value the way the API would have stored it. */
function value(v) {
  if (v === undefined || v === null) return 'nothing'
  if (typeof v === 'string') return `"${v}"`
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}
</script>

<template>
  <p v-if="!changes.length" class="text-sm text-neutral-500">{{ emptyLabel }}</p>
  <ul v-else class="space-y-1 font-mono text-code">
    <li v-for="(c, i) in shown" :key="`${c.path}-${i}`" class="flex gap-2">
      <span :class="tones[c.kind]" aria-hidden="true">{{ marks[c.kind] }}</span>
      <span class="min-w-0">
        <span class="font-medium">{{ c.path }}</span>
        <template v-if="c.kind === 'changed'">
          : <span class="text-neutral-500">{{ value(c.before) }}</span> →
          <span>{{ value(c.after) }}</span>
        </template>
        <template v-else-if="c.kind === 'added'"> = {{ value(c.after) }} </template>
        <template v-else> was {{ value(c.before) }} </template>
      </span>
    </li>
    <li v-if="hidden > 0" class="text-neutral-500">and {{ hidden }} more</li>
  </ul>
</template>
