<script setup>
import { formatCount } from '@/lib/format'

defineProps({
  /** Unit states from GET /overview. */
  services: { type: Array, default: () => [] },
  dhcp: { type: Object, default: () => ({}) },
  dns: { type: Object, default: () => ({}) },
})

const BADGE = {
  active: 'badge-ok',
  inactive: 'badge-warn',
  missing: 'badge-warn',
  unknown: '',
}
</script>

<template>
  <section class="card" aria-labelledby="dash-services">
    <h2 id="dash-services" class="card-title">Services</h2>
    <dl class="kv">
      <template v-for="s in services" :key="s.name">
        <dt>{{ s.name }}</dt>
        <dd>
          <span class="badge" :class="BADGE[s.state] ?? ''">{{ s.state }}</span>
          <span v-if="s.detail && s.state !== 'active'" class="ml-2 text-xs text-neutral-500">
            {{ s.detail }}
          </span>
        </dd>
      </template>

      <dt>DHCP</dt>
      <dd v-if="dhcp.enabled">
        {{ formatCount(dhcp.leases ?? 0) }}
        <template v-if="dhcp.capacity"> of {{ formatCount(dhcp.capacity) }}</template>
        leases ·
        {{ dhcp.scopes ?? 0 }} scope{{ (dhcp.scopes ?? 0) === 1 ? '' : 's' }}
        <template v-if="dhcp.static"> · {{ dhcp.static }} static</template>
      </dd>
      <dd v-else class="text-neutral-500">off</dd>

      <dt>DNS</dt>
      <dd v-if="dns.enabled">
        <span v-if="dns.domain" class="font-mono">{{ dns.domain }}</span>
        <span v-else class="text-neutral-500">no local domain</span>
        <template v-if="dns.upstreams?.length">
          · forwards to <span class="font-mono">{{ dns.upstreams.join(', ') }}</span>
        </template>
        <template v-if="dns.overrides"> · {{ dns.overrides }} host overrides</template>
      </dd>
      <dd v-else class="text-neutral-500">off</dd>
    </dl>
    <p class="mt-3 text-xs">
      <RouterLink to="/services" class="link">Manage DHCP and DNS</RouterLink>
    </p>
  </section>
</template>
