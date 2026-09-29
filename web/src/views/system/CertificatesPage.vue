<script setup>
import { TabsContent } from 'reka-ui'

import AppTabs from '@/components/AppTabs.vue'
import { usePageTabs } from '@/lib/tabs'
import AccountsTab from '@/views/system/certificates/AccountsTab.vue'
import CertificatesTab from '@/views/system/certificates/CertificatesTab.vue'

const { tabs, tab } = usePageTabs()
</script>

<template>
  <AppTabs v-model="tab" :tabs="tabs">
    <TabsContent value="certificates"><CertificatesTab /></TabsContent>
    <TabsContent value="accounts"><AccountsTab /></TabsContent>
  </AppTabs>
</template>
