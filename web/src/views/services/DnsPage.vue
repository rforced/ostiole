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
import BlockingTab from '@/views/services/dns/BlockingTab.vue'
import EnforcementTab from '@/views/services/dns/EnforcementTab.vue'
import OverridesTab from '@/views/services/dns/OverridesTab.vue'
import QueriesTab from '@/views/services/dns/QueriesTab.vue'
import ResolverTab from '@/views/services/dns/ResolverTab.vue'

const auth = useAuthStore()
const config = useConfigStore()
const dns = computed(() => config.ensureServices().dns)
const { state } = useServicesStatus('dns', { load: true })
const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <template #status><StatusBadge v-if="state" :state="state" /></template>
      <ToggleRow
        v-model="dns.enabled"
        variant="switch"
        label="Enabled"
        aria-label="DNS enabled"
        :disabled="auth.readOnly"
      />
    </PageHeader>
    <ServicesStatus service="dns" />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="resolver"><ResolverTab /></TabsContent>
      <TabsContent value="overrides"><OverridesTab /></TabsContent>
      <TabsContent value="blocking"><BlockingTab /></TabsContent>
      <TabsContent value="enforcement"><EnforcementTab /></TabsContent>
      <TabsContent value="queries"><QueriesTab /></TabsContent>
    </AppTabs>
  </div>
</template>
