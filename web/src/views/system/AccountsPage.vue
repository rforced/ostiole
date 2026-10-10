<script setup>
import { TabsContent } from 'reka-ui'
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import AppTabs from '@/components/AppTabs.vue'
import { useTabHash } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import AccountsSection from '@/views/system/AccountsSection.vue'
import AuditLogSection from '@/views/system/AuditLogSection.vue'
import PasswordSection from '@/views/system/PasswordSection.vue'
import TokensSection from '@/views/system/TokensSection.vue'

const auth = useAuthStore()
const all = useRoute().meta.tabs ?? []
// The audit log names who did what and from where, which is for admins.
const tabs = computed(() => (auth.isAdmin ? all : all.filter((t) => t.value !== 'audit')))
const tab = useTabHash(() => tabs.value.map((t) => t.value))
</script>

<template>
  <AppTabs v-if="tabs.length > 1" v-model="tab" :tabs="tabs">
    <TabsContent value="accounts" class="space-y-5">
      <AccountsSection />
      <TokensSection />
      <PasswordSection />
    </TabsContent>
    <TabsContent value="audit"><AuditLogSection /></TabsContent>
  </AppTabs>
  <div v-else class="space-y-5">
    <AccountsSection />
    <TokensSection />
    <PasswordSection />
  </div>
</template>
