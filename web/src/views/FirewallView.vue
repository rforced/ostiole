<script setup>
import { TabsContent } from 'reka-ui'
import { onMounted, ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import { useConfigStore } from '@/stores/config'
import AliasesTab from '@/views/firewall/AliasesTab.vue'
import LogTab from '@/views/firewall/LogTab.vue'
import NatTab from '@/views/firewall/NatTab.vue'
import RulesTab from '@/views/firewall/RulesTab.vue'

const config = useConfigStore()
const tab = ref('rules')
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
        { value: 'nat', label: 'NAT' },
        { value: 'log', label: 'Log' },
      ]"
    >
      <TabsContent value="rules"><RulesTab /></TabsContent>
      <TabsContent value="aliases"><AliasesTab /></TabsContent>
      <TabsContent value="nat"><NatTab /></TabsContent>
      <TabsContent value="log"><LogTab /></TabsContent>
    </AppTabs>
  </div>
</template>
