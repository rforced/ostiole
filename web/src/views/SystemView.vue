<script setup>
import { onMounted } from 'vue'

import { useConfigStore } from '@/stores/config'
import AccessSection from '@/views/system/AccessSection.vue'
import BackupSection from '@/views/system/BackupSection.vue'
import CertificateSection from '@/views/system/CertificateSection.vue'
import ManagementSection from '@/views/system/ManagementSection.vue'
import OsUpdatesSection from '@/views/system/OsUpdatesSection.vue'
import PasswordSection from '@/views/system/PasswordSection.vue'
import RevisionsSection from '@/views/system/RevisionsSection.vue'
import RulesetSection from '@/views/system/RulesetSection.vue'
import UpdatesSection from '@/views/system/UpdatesSection.vue'

const config = useConfigStore()
onMounted(() => config.load())
</script>

<template>
  <div class="space-y-6">
    <h1 class="text-2xl font-semibold tracking-tight">System</h1>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <ManagementSection v-if="config.draft" />
    <PasswordSection />
    <AccessSection />
    <CertificateSection />
    <UpdatesSection />
    <OsUpdatesSection />
    <BackupSection v-if="config.draft" />
    <RevisionsSection v-if="config.draft" />
    <RulesetSection />
  </div>
</template>
