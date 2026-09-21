<script setup>
import { TabsContent } from 'reka-ui'
import { computed } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useServicesStatus } from '@/lib/servicesStatus'
import { usePageTabs } from '@/lib/tabs'
import { useConfigStore } from '@/stores/config'
import ServicesStatus from '@/views/services/ServicesStatus.vue'
import MappingsTab from '@/views/services/upnp/MappingsTab.vue'
import ServiceTab from '@/views/services/upnp/ServiceTab.vue'

const config = useConfigStore()
const upnp = computed(() => config.ensureUPnP())
const { state } = useServicesStatus('upnp', { load: true })
const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <template #status><StatusBadge v-if="state" :state="state" /></template>
      <ToggleRow
        v-model="upnp.enabled"
        variant="switch"
        label="Enabled"
        aria-label="Port mapping enabled"
      />
    </PageHeader>
    <ServicesStatus service="upnp" />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="service"><ServiceTab /></TabsContent>
      <TabsContent value="mappings"><MappingsTab /></TabsContent>
    </AppTabs>
  </div>
</template>
