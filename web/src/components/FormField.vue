<script setup>
import { Label } from 'reka-ui'

defineProps({
  id: { type: String, required: true },
  label: { type: String, required: true },
  hint: { type: String, default: '' },
})
</script>

<template>
  <div class="space-y-1">
    <Label :for="id" class="block text-sm font-medium">{{ label }}</Label>
    <slot />
    <p v-if="hint" class="text-xs text-neutral-500">{{ hint }}</p>
  </div>
</template>
