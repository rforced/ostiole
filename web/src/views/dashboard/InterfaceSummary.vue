<script setup>
import { ArrowDown, ArrowUp } from 'lucide-vue-next'

import SectionCard from '@/components/SectionCard.vue'
import { formatBytes, formatCount, formatRate } from '@/lib/format'

defineProps({
  /** Interface summaries from GET /overview. */
  interfaces: { type: Array, default: () => [] },
  /** Bits per second per interface, once two samples have been seen. */
  rates: { type: Object, default: () => ({}) },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
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

/** Errors since the link came up, or nothing when there are none. */
function faults(l) {
  const errors = (l.rxErrors ?? 0) + (l.txErrors ?? 0)
  if (!errors) return ''
  return `${formatCount(errors)} error${errors === 1 ? '' : 's'}`
}
</script>

<template>
  <SectionCard title="Interfaces" flush>
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
          <td colspan="5" class="text-ink-muted">{{ loaded ? 'No interfaces.' : 'Reading…' }}</td>
        </tr>
        <tr v-for="l in interfaces" :key="l.name" :class="l.configured ? '' : 'opacity-70'">
          <td>
            <div class="font-mono font-medium">{{ l.name }}</div>
            <div class="text-xs text-ink-muted">
              {{ l.description || (l.vlanId ? `VLAN ${l.vlanId}` : l.kind || 'not present') }}
              <template v-if="l.configured && addressing(l)"> · {{ addressing(l) }}</template>
            </div>
          </td>
          <td>
            <span v-if="l.zone" class="font-mono text-code">{{ l.zone }}</span>
            <span v-else class="text-xs text-ink-muted">unmanaged</span>
            <span v-if="l.external" class="badge ml-1">WAN</span>
          </td>
          <td>
            <span v-if="!l.present" class="badge badge-warn">absent</span>
            <span v-else-if="l.configured && !l.enabled" class="badge">disabled</span>
            <span v-else-if="l.carrier" class="badge badge-ok">up</span>
            <span v-else-if="l.up" class="badge badge-warn">no carrier</span>
            <!-- Down is a fault on a link the router is meant to use. -->
            <span v-else class="badge" :class="{ 'badge-warn': l.configured }">down</span>
            <div v-if="faults(l)" class="mt-1 text-warn">
              {{ faults(l) }}
            </div>
          </td>
          <td class="font-mono text-code">
            <div v-for="a in l.addresses" :key="a">{{ a }}</div>
            <div v-for="a in pending(l)" :key="a" class="text-ink-muted">
              {{ a }} <span class="font-sans">(configured)</span>
            </div>
            <span v-if="!l.addresses?.length && !pending(l).length" class="text-ink-muted">—</span>
          </td>
          <td class="text-right font-mono text-code whitespace-nowrap">
            <div v-if="l.present">
              <ArrowDown class="inline size-3" aria-hidden="true" />
              <span class="sr-only">receiving</span>
              {{ rates[l.name] ? formatRate(rates[l.name].rx) : '—' }}
              <span class="ml-1 text-ink-muted">{{ formatBytes(l.rxBytes) }}</span>
            </div>
            <div v-if="l.present">
              <ArrowUp class="inline size-3" aria-hidden="true" />
              <span class="sr-only">sending</span>
              {{ rates[l.name] ? formatRate(rates[l.name].tx) : '—' }}
              <span class="ml-1 text-ink-muted">{{ formatBytes(l.txBytes) }}</span>
            </div>
            <span v-if="!l.present" class="text-ink-muted">—</span>
          </td>
        </tr>
      </TransitionGroup>
    </table>
  </SectionCard>
</template>
