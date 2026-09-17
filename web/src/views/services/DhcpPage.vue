<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import ServicesStatus from '@/views/services/ServicesStatus.vue'
import Ipv4Tab from '@/views/services/dhcp/Ipv4Tab.vue'
import Ipv6Tab from '@/views/services/dhcp/Ipv6Tab.vue'
import LeasesTab from '@/views/services/dhcp/LeasesTab.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-4">
    <ServicesStatus service="dhcp" />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="v4"><Ipv4Tab /></TabsContent>
      <TabsContent value="v6"><Ipv6Tab /></TabsContent>
      <TabsContent value="leases"><LeasesTab /></TabsContent>
    </AppTabs>
  </div>
</template>
