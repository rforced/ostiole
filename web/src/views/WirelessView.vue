<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import ClientsTab from '@/views/wireless/ClientsTab.vue'
import NetworksTab from '@/views/wireless/NetworksTab.vue'
import RadiosTab from '@/views/wireless/RadiosTab.vue'
import WirelessStatus from '@/views/wireless/WirelessStatus.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-4">
    <WirelessStatus />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="radios"><RadiosTab /></TabsContent>
      <TabsContent value="networks"><NetworksTab /></TabsContent>
      <TabsContent value="clients"><ClientsTab /></TabsContent>
    </AppTabs>
  </div>
</template>
