<script setup>
import { computed, ref } from 'vue'

import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

defineProps({
  /** Report on the validating resolver too, which the DNS page can ask for. */
  resolver: { type: Boolean, default: false },
  /** Report on the port mapping service instead of DHCP and DNS. */
  upnp: { type: Boolean, default: false },
})

const config = useConfigStore()
const status = ref(null)

/** The draft asks for the validating resolver (DNSSEC or DNS over TLS). */
const resolverWanted = computed(() => {
  const dns = config.draft?.services?.dns
  return Boolean(dns?.enabled && dns.resolver && dns.resolver !== 'forward')
})

/** The draft asks for port mapping, and something is there to answer. */
const upnpWanted = computed(() => {
  const u = config.draft?.services?.upnp
  return Boolean(u?.enabled && (u.igd || u.pcp))
})

/** A failed read leaves the strip empty; the page works without it. */
useAsync(
  async () => {
    status.value = await api.services.status()
  },
  { immediate: true },
)
</script>

<template>
  <div v-if="status" class="space-y-3">
    <template v-if="upnp">
      <p
        v-if="!status.upnpSetUp && upnpWanted"
        class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
        role="note"
      >
        Port mapping is not set up on this router yet. Run
        <code class="font-mono">ostiole services setup --with-upnp</code> as root once. Until then,
        applying this fails.
      </p>
      <p v-else-if="!status.upnpSetUp" class="text-sm text-neutral-500">
        Not set up on this router. Run
        <code class="font-mono">ostiole services setup --with-upnp</code> as root once.
      </p>
      <p v-else class="text-sm text-neutral-500">
        Port mapping {{ status.upnpRunning ? 'running' : 'stopped' }} ·
        {{ status.mappings }} mapping(s)
      </p>
    </template>
    <template v-else>
      <p
        v-if="!status.setUp"
        class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
        role="note"
      >
        DHCP and DNS are not set up on this router yet. Run
        <code class="font-mono">ostiole services setup</code> as root once. Until then, enabling
        either fails to apply.
      </p>
      <p v-else class="text-sm text-neutral-500">
        DHCP and DNS {{ status.running ? 'running' : 'stopped' }}
        <template v-if="!resolver">· {{ status.leases }} lease(s)</template>
        <template v-if="resolver && status.resolverSetUp">
          · validating resolver {{ status.resolverRunning ? 'running' : 'stopped' }}
        </template>
      </p>
      <p
        v-if="resolver && !status.resolverSetUp && resolverWanted"
        class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
        role="note"
      >
        The validating resolver is not installed. Run
        <code class="font-mono">ostiole services setup --with-resolver</code> as root once. Until
        then, applying this DNS configuration fails.
      </p>
    </template>
  </div>
</template>
