<script setup>
import { TabsContent } from 'reka-ui'
import { onMounted, ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import { useConfigStore } from '@/stores/config'
import CaptureTab from '@/views/diagnostics/CaptureTab.vue'
import JournalTab from '@/views/diagnostics/JournalTab.vue'
import ReachabilityTab from '@/views/diagnostics/ReachabilityTab.vue'

const config = useConfigStore()
const tab = ref('reachability')

onMounted(() => config.load())
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Diagnostics</h1>
    <p class="max-w-3xl text-sm text-neutral-500">
      These run on the firewall itself, so they see what it sees. They need the daemon to be root,
      which it is on an installed box.
    </p>
    <AppTabs
      v-model="tab"
      :tabs="[
        { value: 'reachability', label: 'Ping and traceroute' },
        { value: 'capture', label: 'Packet capture' },
        { value: 'journal', label: 'Logs' },
      ]"
    >
      <TabsContent value="reachability"><ReachabilityTab /></TabsContent>
      <TabsContent value="capture"><CaptureTab /></TabsContent>
      <TabsContent value="journal"><JournalTab /></TabsContent>
    </AppTabs>
  </div>
</template>
