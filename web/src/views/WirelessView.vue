<script setup>
import { onMounted } from 'vue'
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import { usePageTabs } from '@/lib/tabs'
import { useConfigStore } from '@/stores/config'
import ClientsTab from '@/views/wireless/ClientsTab.vue'
import NetworksTab from '@/views/wireless/NetworksTab.vue'
import RadiosTab from '@/views/wireless/RadiosTab.vue'
import WirelessStatus from '@/views/wireless/WirelessStatus.vue'

const { tabs, tab } = usePageTabs()
const config = useConfigStore()

onMounted(() => config.load())
</script>

<template>
  <div class="space-y-5">
    <PageHeader title="Wireless" />
    <p v-if="config.error" role="alert" class="text-sm text-bad">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-ink-muted">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>

    <template v-else-if="config.draft">
      <WirelessStatus />
      <AppTabs v-model="tab" :tabs="tabs">
        <TabsContent value="radios"><RadiosTab /></TabsContent>
        <TabsContent value="networks"><NetworksTab /></TabsContent>
        <TabsContent value="clients"><ClientsTab /></TabsContent>
      </AppTabs>
    </template>
  </div>
</template>
