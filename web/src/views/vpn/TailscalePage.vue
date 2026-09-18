<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import TailscaleStatus from '@/views/vpn/tailscale/TailscaleStatus.vue'
import PeersTab from '@/views/vpn/tailscale/PeersTab.vue'
import SettingsTab from '@/views/vpn/tailscale/SettingsTab.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-4">
    <TailscaleStatus />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="settings"><SettingsTab /></TabsContent>
      <TabsContent value="peers"><PeersTab /></TabsContent>
    </AppTabs>
  </div>
</template>
