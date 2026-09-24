<script setup>
import { TabsContent } from 'reka-ui'
import { computed } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useServicesStatus } from '@/lib/servicesStatus'
import { usePageTabs } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ServicesStatus from '@/views/services/ServicesStatus.vue'
import Ipv4Tab from '@/views/services/dhcp/Ipv4Tab.vue'
import Ipv6Tab from '@/views/services/dhcp/Ipv6Tab.vue'
import LeasesTab from '@/views/services/dhcp/LeasesTab.vue'

const auth = useAuthStore()
const config = useConfigStore()
/** One flag for both families: it governs the pools and the advertisements. */
const dhcp = computed(() => config.ensureServices().dhcp)
const { state } = useServicesStatus('dhcp', { load: true })
const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <template #status><StatusBadge v-if="state" :state="state" /></template>
      <ToggleRow
        v-model="dhcp.enabled"
        variant="switch"
        label="Enabled"
        aria-label="DHCP enabled"
        :disabled="auth.readOnly"
      />
    </PageHeader>
    <ServicesStatus service="dhcp" />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="v4"><Ipv4Tab /></TabsContent>
      <TabsContent value="v6"><Ipv6Tab /></TabsContent>
      <TabsContent value="leases"><LeasesTab /></TabsContent>
    </AppTabs>
  </div>
</template>
