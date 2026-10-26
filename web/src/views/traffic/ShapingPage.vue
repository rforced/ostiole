<script setup lang="ts">
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import BandwidthTab from '@/views/traffic/shaping/BandwidthTab.vue'
import LiveTab from '@/views/traffic/shaping/LiveTab.vue'
import PrioritiesTab from '@/views/traffic/shaping/PrioritiesTab.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <AppTabs v-model="tab" :tabs="tabs">
    <TabsContent value="bandwidth"><BandwidthTab /></TabsContent>
    <TabsContent value="priorities"><PrioritiesTab /></TabsContent>
    <TabsContent value="live"><LiveTab /></TabsContent>
  </AppTabs>
</template>
