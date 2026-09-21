<script setup>
import { ChevronRight } from 'lucide-vue-next'
import { CollapsibleContent, CollapsibleRoot, CollapsibleTrigger } from 'reka-ui'

/** The fold at the end of a form card for the fields most routers leave alone. */
defineProps({
  label: { type: String, default: 'Advanced' },
})
const open = defineModel('open', { type: Boolean, default: false })
</script>

<template>
  <CollapsibleRoot v-model:open="open" class="space-y-4">
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
