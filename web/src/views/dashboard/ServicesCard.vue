<script setup>
import SectionCard from '@/components/SectionCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatCount } from '@/lib/format'

defineProps({
  /** Unit states from GET /overview. */
  services: { type: Array, default: () => [] },
  dhcp: { type: Object, default: () => ({}) },
  dns: { type: Object, default: () => ({}) },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many units to hold room for until then. */
  placeholders: { type: Number, default: 3 },
})

/** The service pages' words: off when the configuration leaves it off. */
function word(s) {
  if (s.state === 'active') return 'running'
  if (s.state === 'inactive') return s.want ? 'stopped' : 'off'
  return s.state
}
</script>

<template>
  <SectionCard title="Services" :aria-busy="loaded ? undefined : 'true'">
    <template v-if="!loaded">
      <p class="sr-only">Reading…</p>
      <!-- How many units there are depends on what is set up; the two
           summaries are always there. -->
      <dl class="kv" aria-hidden="true" data-reading>
        <template v-for="n in placeholders" :key="n">
          <dt><span class="skeleton w-28"></span></dt>
          <dd><span class="skeleton h-5 w-14 rounded-full"></span></dd>
        </template>
        <dt>DHCP</dt>
        <dd><span class="skeleton w-48"></span></dd>
        <dt>DNS</dt>
        <dd><span class="skeleton w-56"></span></dd>
      </dl>
    </template>
    <dl v-else class="kv">
      <template v-for="s in services" :key="s.name">
        <dt>{{ s.name }}</dt>
        <dd>
          <StatusBadge :state="word(s)" />
          <span v-if="s.detail && s.state !== 'active'" class="ml-2 text-ink-muted">
            {{ s.detail }}
          </span>
        </dd>
      </template>

      <dt>DHCP</dt>
      <dd v-if="dhcp.enabled">
        {{ formatCount(dhcp.leases ?? 0) }}
        <template v-if="dhcp.capacity"> of {{ formatCount(dhcp.capacity) }}</template>
        leases ·
        {{ dhcp.servers ?? 0 }} server{{ (dhcp.servers ?? 0) === 1 ? '' : 's' }}
        <template v-if="dhcp.static"> · {{ dhcp.static }} static</template>
      </dd>
      <dd v-else class="text-ink-muted">off</dd>

      <dt>DNS</dt>
      <dd v-if="dns.enabled">
        <span v-if="dns.domain" class="font-mono">{{ dns.domain }}</span>
        <span v-else class="text-ink-muted">no local domain</span>
        <template v-if="dns.resolver === 'recursive'"> · recursive</template>
        <template v-else-if="dns.upstreams?.length">
          · {{ dns.resolver === 'tls' ? 'DNS over TLS to' : 'forwards to' }}
          <span class="font-mono">{{ dns.upstreams.join(', ') }}</span>
        </template>
        <template v-if="dns.overrides"> · {{ dns.overrides }} host overrides</template>
      </dd>
      <dd v-else class="text-ink-muted">off</dd>
    </dl>
    <p class="mt-3">
      <RouterLink to="/services" class="link">Manage DHCP and DNS</RouterLink>
    </p>
  </SectionCard>
</template>
