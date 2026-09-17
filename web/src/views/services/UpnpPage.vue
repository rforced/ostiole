<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import ServicesStatus from '@/views/services/ServicesStatus.vue'
import MappingsTab from '@/views/services/upnp/MappingsTab.vue'
import ServiceTab from '@/views/services/upnp/ServiceTab.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-4">
    <ServicesStatus upnp />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="service"><ServiceTab /></TabsContent>
      <TabsContent value="mappings"><MappingsTab /></TabsContent>
    </AppTabs>
  </div>
</template>
