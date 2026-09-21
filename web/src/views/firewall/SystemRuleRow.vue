<script setup>
import { Lock } from 'lucide-vue-next'

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
</script>

<template>
  <tr class="row-system" data-system>
    <td>
      <span role="img" aria-label="System rule" title="Added by Ostiole"
        ><Lock class="size-4" aria-hidden="true"
      /></span>
    </td>
    <td>
      <span
        class="badge"
        :class="{ 'badge-ok': rule.action === 'accept', 'badge-warn': rule.action === 'drop' }"
        >{{ rule.action }}</span
      >
      <span v-if="rule.log" class="badge ml-1">log</span>
    </td>
    <td class="font-mono text-code">{{ rule.protocol }}</td>
    <td class="font-mono text-code">{{ rule.source }}</td>
    <td class="font-mono text-code">{{ rule.destination }}</td>
    <td></td>
    <td>{{ rule.description }}</td>
    <td class="text-right font-mono text-code tabular-nums">{{ packets }}</td>
    <td class="text-right whitespace-nowrap">
      <RouterLink v-if="to" :to="to" class="link">Edit</RouterLink>
    </td>
  </tr>
</template>
