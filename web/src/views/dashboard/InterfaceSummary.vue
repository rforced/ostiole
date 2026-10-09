<script setup>
import { ArrowDown, ArrowUp } from '@lucide/vue'

import LinkErrors from '@/components/LinkErrors.vue'
import SectionCard from '@/components/SectionCard.vue'
import { formatBytes, formatRate } from '@/lib/format'

defineProps({
  /** Interface summaries from GET /overview. */
  interfaces: { type: Array, default: () => [] },
  /** Bits per second per interface, once two samples have been seen. */
  rates: { type: Object, default: () => ({}) },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many rows to hold room for until then. */
  placeholders: { type: Number, default: 3 },
})

/** How the interface is addressed: "IPv4 static · IPv6 slaac". */
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
  <SectionCard title="Interfaces" flush :aria-busy="loaded ? undefined : 'true'">
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Interface</th>
          <th>Zone</th>
          <th>Link</th>
          <th>Addresses</th>
          <th class="num">Traffic</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!loaded && !placeholders" data-reading>
          <td colspan="5">
            <span class="sr-only">Reading…</span><span class="skeleton w-28"></span>
          </td>
        </tr>
        <template v-else-if="!loaded">
          <tr v-for="n in placeholders" :key="`reading-${n}`" data-reading>
            <td data-label="">
              <span v-if="n === 1" class="sr-only">Reading…</span>
              <div><span class="skeleton w-14"></span></div>
              <div><span class="skeleton h-3 w-32"></span></div>
            </td>
            <td data-label="Zone"><span class="skeleton w-10"></span></td>
            <td data-label="Link"><span class="skeleton h-5 w-9 rounded-full"></span></td>
            <td data-label="Addresses"><span class="skeleton w-28"></span></td>
            <td class="num max-sm:text-left" data-label="Traffic">
              <div><span class="skeleton w-24"></span></div>
              <div><span class="skeleton w-24"></span></div>
            </td>
          </tr>
        </template>
        <tr v-else-if="!interfaces.length">
          <td colspan="5" class="text-ink-muted">No interfaces.</td>
        </tr>
        <tr v-for="l in interfaces" :key="l.name" :class="l.configured ? '' : 'opacity-70'">
          <td data-label="">
            <div class="font-mono font-medium">{{ l.name }}</div>
            <div class="text-xs text-ink-muted">
              {{ l.description || (l.vlanId ? `VLAN ${l.vlanId}` : l.kind || 'not present') }}
              <template v-if="l.configured && addressing(l)"> · {{ addressing(l) }}</template>
            </div>
          </td>
          <td data-label="Zone">
            <span class="inline-flex items-center gap-1.5">
              <span v-if="l.zone" class="font-mono text-code">{{ l.zone }}</span>
              <span v-else class="text-xs text-ink-muted">unmanaged</span>
              <span v-if="l.external" class="badge">WAN</span>
            </span>
          </td>
          <td data-label="Link">
            <span v-if="!l.present" class="badge badge-warn">absent</span>
            <span v-else-if="l.configured && !l.enabled" class="badge">disabled</span>
            <span v-else-if="l.carrier" class="badge badge-ok">up</span>
            <span v-else-if="l.up" class="badge badge-warn">no carrier</span>
            <!-- Down is a fault on a link the router is meant to use. -->
            <span v-else class="badge" :class="{ 'badge-warn': l.configured }">down</span>
            <LinkErrors :link="l" />
          </td>
          <td class="font-mono text-code" data-label="Addresses">
            <div v-for="a in l.addresses" :key="a">{{ a }}</div>
            <div v-for="a in pending(l)" :key="a" class="text-ink-muted">
              {{ a }} <span class="font-sans">(configured)</span>
            </div>
            <span v-if="!l.addresses?.length && !pending(l).length" class="text-ink-muted">—</span>
          </td>
          <td class="num font-mono text-code max-sm:text-left" data-label="Traffic">
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
      </tbody>
    </table>
  </SectionCard>
</template>
