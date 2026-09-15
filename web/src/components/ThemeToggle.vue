<script setup>
import { Monitor, Moon, Sun } from 'lucide-vue-next'
import { ToggleGroupItem, ToggleGroupRoot } from 'reka-ui'

import { isThemePreference, useThemeStore } from '@/stores/theme'

const theme = useThemeStore()

const options = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
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
    class="inline-flex rounded-md border border-neutral-200 bg-neutral-50 p-0.5 dark:border-neutral-800 dark:bg-neutral-900"
    @update:model-value="onChange"
  >
    <ToggleGroupItem
      v-for="opt in options"
      :key="opt.value"
      :value="opt.value"
      :aria-label="opt.label"
      :title="opt.label"
      class="rounded p-1.5 text-neutral-500 transition-colors hover:text-neutral-900 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:outline-none data-[state=on]:bg-white data-[state=on]:text-neutral-900 data-[state=on]:shadow-sm dark:hover:text-neutral-100 dark:data-[state=on]:bg-neutral-800 dark:data-[state=on]:text-neutral-100"
    >
      <component :is="opt.icon" class="size-4" aria-hidden="true" />
    </ToggleGroupItem>
  </ToggleGroupRoot>
</template>
