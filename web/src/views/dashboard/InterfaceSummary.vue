<script setup>
import { ArrowDown, ArrowUp } from 'lucide-vue-next'

import { formatBytes } from '@/lib/format'

defineProps({
  /** Interface summaries from GET /overview. */
  interfaces: { type: Array, default: () => [] },
})

/** How the interface is addressed, e.g. "IPv4 static · IPv6 slaac". */
function addressing(l) {
  const parts = []
  if (l.ipv4Mode && l.ipv4Mode !== 'none') parts.push(`IPv4 ${l.ipv4Mode}`)
  if (l.ipv6Mode && l.ipv6Mode !== 'none') parts.push(`IPv6 ${l.ipv6Mode}`)
  return parts.join(' · ')
}

/** Static addresses the configuration asks for that the kernel does not have. */
function pending(l) {
  return (l.configuredAddresses ?? []).filter((a) => !(l.addresses ?? []).includes(a))
}
</script>

<template>
  <section class="card" aria-labelledby="dash-interfaces">
    <h2 id="dash-interfaces" class="card-title">Interfaces</h2>
    <div class="-mx-4 -mb-4 overflow-x-auto">
      <table class="table">
        <thead>
          <tr>
            <th>Interface</th>
            <th>Zone</th>
            <th>Link</th>
            <th>Addresses</th>
            <th class="text-right">Traffic</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!interfaces.length" key="empty" class="row-static">
            <td colspan="5" class="text-neutral-500">No interfaces yet.</td>
          </tr>
          <tr v-for="l in interfaces" :key="l.name" :class="l.configured ? '' : 'opacity-70'">
            <td>
              <div class="font-mono font-medium">{{ l.name }}</div>
              <div class="text-xs text-neutral-500">
                {{ l.description || (l.vlanId ? `VLAN ${l.vlanId}` : l.kind || 'not present') }}
                <template v-if="l.configured && addressing(l)"> · {{ addressing(l) }}</template>
              </div>
            </td>
            <td>
              <span v-if="l.zone" class="font-mono text-code">{{ l.zone }}</span>
              <span v-else class="text-xs text-neutral-500">unmanaged</span>
              <span v-if="l.external" class="badge ml-1">WAN</span>
            </td>
            <td>
              <span v-if="!l.present" class="badge badge-warn">absent</span>
              <span v-else-if="l.configured && !l.enabled" class="badge">disabled</span>
              <span v-else-if="l.carrier" class="badge badge-ok">up</span>
              <span v-else-if="l.up" class="badge badge-warn">no carrier</span>
              <span v-else class="badge">down</span>
            </td>
            <td class="font-mono text-code">
              <div v-for="a in l.addresses" :key="a">{{ a }}</div>
              <div v-for="a in pending(l)" :key="a" class="text-neutral-500">
                {{ a }} <span class="font-sans">(configured)</span>
              </div>
              <span v-if="!l.addresses?.length && !pending(l).length" class="text-neutral-500"
                >—</span
              >
            </td>
            <td class="text-right font-mono text-code whitespace-nowrap">
              <div v-if="l.present">
                <ArrowDown class="inline size-3" aria-hidden="true" />{{ formatBytes(l.rxBytes) }}
                <span class="sr-only">received,</span>
              </div>
              <div v-if="l.present">
                <ArrowUp class="inline size-3" aria-hidden="true" />{{ formatBytes(l.txBytes) }}
                <span class="sr-only">sent</span>
              </div>
              <span v-if="!l.present" class="text-neutral-500">—</span>
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
  </section>
</template>
