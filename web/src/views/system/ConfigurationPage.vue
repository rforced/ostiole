<script setup>
import { TabsContent } from 'reka-ui'
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import AppTabs from '@/components/AppTabs.vue'
import { useTabHash } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import BackupSection from '@/views/system/BackupSection.vue'
import RemoteBackupSection from '@/views/system/RemoteBackupSection.vue'
import RevisionsSection from '@/views/system/RevisionsSection.vue'

const auth = useAuthStore()
const all = useRoute().meta.tabs ?? []
// A viewer can neither download nor restore, so the tab would be empty.
const tabs = computed(() => (auth.readOnly ? all.filter((t) => t.value !== 'backup') : all))
const tab = useTabHash(() => tabs.value.map((t) => t.value))
</script>

<template>
  <AppTabs v-model="tab" :tabs="tabs">
    <TabsContent value="history"><RevisionsSection /></TabsContent>
    <TabsContent value="backup"><BackupSection /></TabsContent>
    <TabsContent value="remote"><RemoteBackupSection /></TabsContent>
  </AppTabs>
</template>
