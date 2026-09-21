<script setup>
import { Monitor, Moon, Sun } from 'lucide-vue-next'
import { ToggleGroupItem, ToggleGroupRoot } from 'reka-ui'

import { isThemePreference, useThemeStore } from '@/stores/theme'

defineProps({
  /** Fills its row, with the three choices sharing it equally. */
  block: { type: Boolean, default: false },
})

const theme = useThemeStore()

const options = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'system', label: 'System', icon: Monitor },
  { value: 'dark', label: 'Dark', icon: Moon },
]

function onChange(value) {
  // Single-select toggle groups emit undefined when the active item is clicked again; keep the current value.
  if (isThemePreference(value)) theme.set(value)
}
</script>

<template>
  <ToggleGroupRoot
    :model-value="theme.preference"
    type="single"
    aria-label="Color theme"
    class="rounded-md border border-line bg-surface-2 p-0.5"
    :class="block ? 'flex' : 'inline-flex'"
    @update:model-value="onChange"
  >
    <ToggleGroupItem
      v-for="opt in options"
      :key="opt.value"
      :value="opt.value"
      :aria-label="opt.label"
      :title="opt.label"
      class="flex items-center justify-center rounded p-1.5 text-ink-muted transition-colors hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none data-[state=on]:bg-surface data-[state=on]:text-ink data-[state=on]:shadow-sm"
      :class="{ 'flex-1': block }"
    >
      <component :is="opt.icon" class="size-4" aria-hidden="true" />
    </ToggleGroupItem>
  </ToggleGroupRoot>
</template>
