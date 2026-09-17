<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import ServicesStatus from '@/views/services/ServicesStatus.vue'
import BlockListsTab from '@/views/services/dns/BlockListsTab.vue'
import EnforcementTab from '@/views/services/dns/EnforcementTab.vue'
import ExceptionsTab from '@/views/services/dns/ExceptionsTab.vue'
import ResolverTab from '@/views/services/dns/ResolverTab.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-4">
    <ServicesStatus resolver />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="server"><ResolverTab /></TabsContent>
      <TabsContent value="lists"><BlockListsTab /></TabsContent>
      <TabsContent value="exceptions"><ExceptionsTab /></TabsContent>
      <TabsContent value="enforcement"><EnforcementTab /></TabsContent>
    </AppTabs>
  </div>
</template>
