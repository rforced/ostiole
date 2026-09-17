<script setup>
import { computed, onMounted, ref } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

defineProps({
  /** Report on unbound too, the validating resolver the DNS page can ask for. */
  resolver: { type: Boolean, default: false },
})

const config = useConfigStore()
const status = ref(null)

/** The draft asks for unbound (DNSSEC validation or DNS over TLS). */
const resolverWanted = computed(() => {
  const dns = config.draft?.services?.dns
  return Boolean(dns?.enabled && dns.resolver && dns.resolver !== 'forward')
})

onMounted(async () => {
  try {
    status.value = await api.services.status()
  } catch {
    status.value = null
  }
})
</script>

<template>
  <div v-if="status" class="space-y-3">
    <p
      v-if="!status.setUp"
      class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
      role="note"
    >
      dnsmasq is not set up on this box yet. Run
      <code class="font-mono">ostiole services setup</code> as root once; until then, enabling this
      service fails to apply.
    </p>
    <p v-else class="text-sm text-neutral-500">
      dnsmasq {{ status.running ? 'running' : 'stopped' }}
      <template v-if="!resolver">· {{ status.leases }} lease(s)</template>
      <template v-if="resolver && status.resolverSetUp">
        · unbound {{ status.resolverRunning ? 'running' : 'stopped' }}
      </template>
    </p>
    <p
      v-if="resolver && !status.resolverSetUp && resolverWanted"
      class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
      role="note"
    >
      The validating resolver is not installed. Run
      <code class="font-mono">ostiole services setup --with-resolver</code> as root once; until
      then, applying this DNS configuration fails.
    </p>
  </div>
</template>
