<script setup lang="ts">
import { GripVertical } from '@lucide/vue'

import ToggleRow from '@/components/ToggleRow.vue'

defineProps({
  /** The title the card's header shows. */
  title: { type: String, required: true },
  /** Whether the dashboard draws the card. */
  shown: { type: Boolean, default: true },
  /** Whether this is the row being dragged. */
  dragging: { type: Boolean, default: false },
})
defineEmits(['toggle', 'grab'])
</script>

<template>
  <li class="flex items-center gap-1 rounded-md py-1 pr-2" :class="{ 'bg-surface-2': dragging }">
    <!-- Dragged by pointer only. A finger on it drags rather than scrolls. -->
    <span
      class="flex cursor-grab touch-none items-center justify-center rounded-md p-1.5 text-ink-muted hover:text-ink max-sm:min-h-11 max-sm:min-w-11"
      aria-hidden="true"
      data-handle
      @pointerdown="$emit('grab', $event)"
    >
      <GripVertical class="size-4" />
    </span>
    <ToggleRow
      class="min-w-0 flex-1 justify-between"
      :class="{ 'text-ink-muted': !shown }"
      :label="title"
      variant="switch"
      :aria-label="`Show ${title}`"
      :model-value="shown"
      @update:model-value="$emit('toggle')"
    />
  </li>
</template>
