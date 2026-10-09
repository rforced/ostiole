<script setup>
import { TabsContent } from 'reka-ui'
import { onMounted, ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import PageHeader from '@/components/PageHeader.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { usePageTabs } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import GatewayEvents from '@/views/routing/GatewayEvents.vue'
import GatewaysTab from '@/views/routing/GatewaysTab.vue'
import RoutesTab from '@/views/routing/RoutesTab.vue'

const auth = useAuthStore()
const config = useConfigStore()
const { tabs, tab } = usePageTabs()
const live = ref([])
const policy = ref([])
const replies = ref([])
const detected = ref([])

/** Each read is on its own: a monitor that is down leaves only its column blank. */
const refresh = useAsync(
  async () => {
    try {
      live.value = await api.gateways()
    } catch {
      live.value = []
    }
    try {
      policy.value = await api.policy()
    } catch {
      policy.value = []
    }
    try {
      replies.value = await api.policyReplies()
    } catch {
      replies.value = []
    }
    try {
      detected.value = await api.detectedGateways()
    } catch {
      detected.value = []
    }
  },
  { interval: 10_000 },
)

onMounted(async () => {
  await config.load()
  await refresh.run()
})
</script>

<template>
  <div class="space-y-5">
    <PageHeader title="Routing">
      <RefreshButton
        :busy="refresh.busy.value"
        :updated-at="refresh.updatedAt.value"
        @click="refresh.run"
      />
    </PageHeader>
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
      <TabsContent value="gateways">
        <GatewaysTab :live="live" :policy="policy" :detected="detected" :refresh="refresh" />
      </TabsContent>
      <TabsContent value="routes"><RoutesTab :replies="replies" /></TabsContent>
      <TabsContent value="events"><GatewayEvents /></TabsContent>
    </AppTabs>
  </div>
</template>
