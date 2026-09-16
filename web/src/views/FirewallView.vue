<script setup>
import { TabsContent } from 'reka-ui'
import { onMounted } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import { useTabHash } from '@/lib/tabs'
import { useConfigStore } from '@/stores/config'
import AliasesTab from '@/views/firewall/AliasesTab.vue'
import LogTab from '@/views/firewall/LogTab.vue'
import NatTab from '@/views/firewall/NatTab.vue'
import RulesTab from '@/views/firewall/RulesTab.vue'
import SchedulesTab from '@/views/firewall/SchedulesTab.vue'

const config = useConfigStore()
const tab = useTabHash(['rules', 'aliases', 'schedules', 'nat', 'log'])
onMounted(() => config.load())
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Firewall</h1>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>
    <AppTabs
      v-else-if="config.draft"
      v-model="tab"
      :tabs="[
        { value: 'rules', label: 'Rules' },
        { value: 'aliases', label: 'Aliases' },
        { value: 'schedules', label: 'Schedules' },
        { value: 'nat', label: 'NAT' },
        { value: 'log', label: 'Log' },
      ]"
    >
      <TabsContent value="rules"><RulesTab /></TabsContent>
      <TabsContent value="aliases"><AliasesTab /></TabsContent>
      <TabsContent value="schedules"><SchedulesTab /></TabsContent>
      <TabsContent value="nat"><NatTab /></TabsContent>
      <TabsContent value="log"><LogTab /></TabsContent>
    </AppTabs>
  </div>
</template>
