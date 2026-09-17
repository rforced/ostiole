<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { useTabHash } from '@/lib/tabs'
import BlockListsTab from '@/views/services/dns/BlockListsTab.vue'
import EnforcementTab from '@/views/services/dns/EnforcementTab.vue'
import ExceptionsTab from '@/views/services/dns/ExceptionsTab.vue'
import ResolverTab from '@/views/services/dns/ResolverTab.vue'

const tab = useTabHash(['server', 'lists', 'exceptions', 'enforcement'])
</script>

<template>
  <AppTabs
    v-model="tab"
    :tabs="[
      { value: 'server', label: 'Server' },
      { value: 'lists', label: 'Block lists' },
      { value: 'exceptions', label: 'Exceptions' },
      { value: 'enforcement', label: 'Enforcement' },
    ]"
  >
    <TabsContent value="server"><ResolverTab /></TabsContent>
    <TabsContent value="lists"><BlockListsTab /></TabsContent>
    <TabsContent value="exceptions"><ExceptionsTab /></TabsContent>
    <TabsContent value="enforcement"><EnforcementTab /></TabsContent>
  </AppTabs>
</template>
