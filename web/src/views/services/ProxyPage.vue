<script setup>
import { TabsContent } from 'reka-ui'
import { computed } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useProxyStatus } from '@/lib/proxyStatus'
import { usePageTabs } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import EventsTab from '@/views/services/proxy/EventsTab.vue'
import PoolsTab from '@/views/services/proxy/PoolsTab.vue'
import ProxyStatus from '@/views/services/proxy/ProxyStatus.vue'
import RoutesTab from '@/views/services/proxy/RoutesTab.vue'
import ServiceTab from '@/views/services/proxy/ServiceTab.vue'
import SitesTab from '@/views/services/proxy/SitesTab.vue'
import WafTab from '@/views/services/proxy/WafTab.vue'

const auth = useAuthStore()
const config = useConfigStore()
const proxy = computed(() => config.proxy)
const { state } = useProxyStatus()
const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <template #status><StatusBadge v-if="state" :state="state" /></template>
      <ToggleRow
        :model-value="proxy.enabled === true"
        variant="switch"
        label="Enabled"
        aria-label="Proxy enabled"
        :disabled="auth.readOnly"
        @update:model-value="config.setProxy({ enabled: $event })"
      />
    </PageHeader>
    <ProxyStatus />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="service"><ServiceTab /></TabsContent>
      <TabsContent value="sites"><SitesTab /></TabsContent>
      <TabsContent value="pools"><PoolsTab /></TabsContent>
      <TabsContent value="waf"><WafTab /></TabsContent>
      <TabsContent value="routes"><RoutesTab /></TabsContent>
      <TabsContent value="events"><EventsTab /></TabsContent>
    </AppTabs>
  </div>
</template>
