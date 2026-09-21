<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { usePageTabs } from '@/lib/tabs'
import { useTailscaleStatus } from '@/lib/tailscaleStatus'
import PeersTab from '@/views/vpn/tailscale/PeersTab.vue'
import SettingsTab from '@/views/vpn/tailscale/SettingsTab.vue'
import TailscaleStatus from '@/views/vpn/tailscale/TailscaleStatus.vue'

const { state } = useTailscaleStatus()
const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <template #status><StatusBadge v-if="state" :state="state" /></template>
    </PageHeader>
    <TailscaleStatus />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="settings"><SettingsTab /></TabsContent>
      <TabsContent value="peers"><PeersTab /></TabsContent>
    </AppTabs>
  </div>
</template>
