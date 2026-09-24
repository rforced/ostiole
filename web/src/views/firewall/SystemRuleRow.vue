<script setup>
import { Lock } from 'lucide-vue-next'

import { useAuthStore } from '@/stores/auth'

/**
 * A rule Ostiole adds on its own, shown among the zone's rules for the order
 * it is evaluated in. It cannot be toggled, moved, or edited here; the link
 * goes to the setting that controls it.
 */
defineProps({
  /** @type {import('vue').PropType<{action: string, protocol: string, source: string, destination: string, description: string, log?: boolean}>} */
  rule: { type: Object, required: true },
  /** The packet count, or an empty string when the rule keeps none. */
  packets: { type: [Number, String], default: '' },
  /** Route of the page that controls the rule, if any. */
  to: { type: String, default: '' },
})

const auth = useAuthStore()
</script>

<template>
  <tr class="row-system max-sm:relative max-sm:pl-12" data-system>
    <td class="max-sm:absolute max-sm:top-3.5 max-sm:left-4">
      <span role="img" aria-label="System rule" title="Added by Ostiole"
        ><Lock class="size-4" aria-hidden="true"
      /></span>
    </td>
    <td class="max-sm:order-2">
      <span
        class="badge"
        :class="{ 'badge-ok': rule.action === 'accept', 'badge-warn': rule.action === 'drop' }"
        >{{ rule.action }}</span
      >
      <span v-if="rule.log" class="badge ml-1">log</span>
    </td>
    <td class="font-mono text-code max-sm:order-3">{{ rule.protocol }}</td>
    <td class="font-mono text-code max-sm:order-3">{{ rule.source }}</td>
    <td class="font-mono text-code max-sm:order-3 max-sm:before:mr-2 max-sm:before:content-['→']">
      {{ rule.destination }}
    </td>
    <td class="max-sm:hidden"></td>
    <td class="max-sm:order-1 max-sm:basis-full">{{ rule.description }}</td>
    <td class="text-right font-mono text-code tabular-nums max-sm:hidden">{{ packets }}</td>
    <td class="text-right whitespace-nowrap max-sm:order-4 max-sm:basis-full max-sm:text-left">
      <RouterLink v-if="to" :to="to" class="link">{{ auth.readOnly ? 'View' : 'Edit' }}</RouterLink>
    </td>
  </tr>
</template>
