<script setup>
import { TabsContent } from 'reka-ui'
import { onMounted, ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import DhcpTab from '@/views/services/DhcpTab.vue'
import DnsTab from '@/views/services/DnsTab.vue'
import LeasesTab from '@/views/services/LeasesTab.vue'

const config = useConfigStore()
const tab = ref('dhcp')
const status = ref(null)

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
    <h1 class="text-2xl font-semibold tracking-tight">Services</h1>
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
      </p>
      <AppTabs
        v-model="tab"
        :tabs="[
          { value: 'dhcp', label: 'DHCP' },
          { value: 'dns', label: 'DNS' },
          { value: 'leases', label: 'Leases' },
        ]"
      >
        <TabsContent value="dhcp"><DhcpTab /></TabsContent>
        <TabsContent value="dns"><DnsTab /></TabsContent>
        <TabsContent value="leases"><LeasesTab /></TabsContent>
      </AppTabs>
    </template>
  </div>
</template>
