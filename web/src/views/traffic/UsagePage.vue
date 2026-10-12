<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import DestinationsTab from '@/views/traffic/DestinationsTab.vue'
import DevicesTab from '@/views/traffic/DevicesTab.vue'
import InterfacesTab from '@/views/traffic/InterfacesTab.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <AppTabs v-model="tab" :tabs="tabs">
    <TabsContent value="interfaces"><InterfacesTab /></TabsContent>
    <TabsContent value="devices"><DevicesTab /></TabsContent>
    <TabsContent value="destinations"><DestinationsTab /></TabsContent>
  </AppTabs>
</template>
