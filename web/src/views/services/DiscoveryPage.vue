<script setup>
import { TabsContent } from 'reka-ui'
import { computed } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useServicesStatus } from '@/lib/servicesStatus'
import { usePageTabs } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import AnnouncementsTab from '@/views/services/discovery/AnnouncementsTab.vue'
import LogTab from '@/views/services/discovery/LogTab.vue'
import SettingsTab from '@/views/services/discovery/SettingsTab.vue'

const auth = useAuthStore()
const config = useConfigStore()
const enabled = computed({
  get: () => config.ensureDiscovery().enabled,
  set: (v) => config.setDiscovery({ enabled: v }),
})
const { state } = useServicesStatus('discovery', { load: true })
const { tabs, tab } = usePageTabs()
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <template #status><StatusBadge v-if="state" :state="state" /></template>
      <ToggleRow
        v-model="enabled"
        variant="switch"
        label="Enabled"
        aria-label="Discovery enabled"
        :disabled="auth.readOnly"
      />
    </PageHeader>
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="settings"><SettingsTab /></TabsContent>
      <TabsContent value="log"><LogTab /></TabsContent>
      <TabsContent value="announcements"><AnnouncementsTab /></TabsContent>
    </AppTabs>
  </div>
</template>
