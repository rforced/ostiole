<script setup>
import { ChevronRight } from 'lucide-vue-next'
import { CollapsibleContent, CollapsibleRoot, CollapsibleTrigger } from 'reka-ui'
import { computed } from 'vue'

import { useLocked } from '@/lib/locked'

/**
 * The fold at the end of a form card for the fields most routers leave
 * alone. In a locked card it stays open: its trigger is disabled with the
 * fields, and what is folded away would be out of reach.
 */
defineProps({
  label: { type: String, default: 'Advanced' },
})
const open = defineModel('open', { type: Boolean, default: false })
const locked = useLocked()
const shown = computed({
  get: () => open.value || locked.value,
  set: (v) => (open.value = v),
})
</script>

<template>
  <CollapsibleRoot v-model:open="shown" class="space-y-4">
    <CollapsibleTrigger
      class="group -ml-1 flex items-center gap-1 rounded px-1 text-sm font-medium text-ink-2 hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
    >
      <ChevronRight
        class="size-4 transition-transform group-data-[state=open]:rotate-90"
        aria-hidden="true"
      />
      {{ label }}
    </CollapsibleTrigger>
    <CollapsibleContent class="space-y-4">
      <slot />
    </CollapsibleContent>
  </CollapsibleRoot>
</template>
