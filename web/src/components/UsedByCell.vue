<script setup lang="ts">
import type { PropType } from 'vue'

import { someOf } from '@/lib/lists'

/**
 * A table's Used by column: what names the row's thing. The row's Delete
 * is off while this lists anything, so the reason sits beside it.
 */
defineProps({
  names: { type: Array as PropType<string[]>, required: true },
})
</script>

<template>
  <td
    class="font-mono text-code"
    :class="{ 'max-sm:hidden': !names.length }"
    data-label="Used by"
    :title="names.length > 1 ? names.join(', ') : undefined"
  >
    {{ someOf(names, 1) || '—' }}
  </td>
</template>
