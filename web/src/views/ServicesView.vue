<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const route = useRoute()
const status = ref(null)

/** The draft asks for unbound (DNSSEC validation or DNS over TLS). */
const resolverWanted = computed(() => {
  const dns = config.draft?.services?.dns
  return Boolean(dns?.enabled && dns.resolver && dns.resolver !== 'forward')
})

onMounted(async () => {
  await config.load()
  try {
    status.value = await api.services.status()
  } catch {
    status.value = null
  }
})
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">{{ route.meta.title }}</h1>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>
    <template v-else-if="config.draft">
      <p
        v-if="status && !status.setUp"
        class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
        role="note"
      >
        dnsmasq is not set up on this box yet. Run
        <code class="font-mono">ostiole services setup</code> as root once; until then, enabling
        these services fails to apply.
      </p>
      <p v-else-if="status" class="text-sm text-neutral-500">
        dnsmasq {{ status.running ? 'running' : 'stopped' }} · {{ status.leases }} lease(s)
        <template v-if="status.resolverSetUp">
          · unbound {{ status.resolverRunning ? 'running' : 'stopped' }}
        </template>
      </p>
      <p
        v-if="status && !status.resolverSetUp && resolverWanted"
        class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
        role="note"
      >
        The validating resolver is not installed. Run
        <code class="font-mono">ostiole services setup --with-resolver</code> as root once; until
        then, applying this DNS configuration fails.
      </p>
      <RouterView />
    </template>
  </div>
</template>
