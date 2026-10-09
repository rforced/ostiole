<script setup>
import { onMounted } from 'vue'
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import PageHeader from '@/components/PageHeader.vue'
import { usePageTabs } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import DestinationsTab from '@/views/traffic/DestinationsTab.vue'
import DevicesTab from '@/views/traffic/DevicesTab.vue'
import InterfacesTab from '@/views/traffic/InterfacesTab.vue'

const { tabs, tab } = usePageTabs()
const auth = useAuthStore()
const config = useConfigStore()

onMounted(() => config.load())
</script>

<template>
  <div class="space-y-5">
    <PageHeader title="Traffic" />
    <ErrorLine v-if="config.error" class="text-sm">
      {{ config.error }}
    </ErrorLine>
    <p v-if="config.loaded && !config.draft" class="text-sm text-ink-muted">
      No configuration yet.
      <template v-if="!auth.readOnly">
        <RouterLink to="/wizard" class="link">Run the setup wizard</RouterLink> first.
      </template>
    </p>
    <AppTabs v-else-if="config.draft" v-model="tab" :tabs="tabs">
      <TabsContent value="interfaces"><InterfacesTab /></TabsContent>
      <TabsContent value="devices"><DevicesTab /></TabsContent>
      <TabsContent value="destinations"><DestinationsTab /></TabsContent>
    </AppTabs>
  </div>
</template>
