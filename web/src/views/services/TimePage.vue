<script setup>
import AppNotice from '@/components/AppNotice.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { useNtpStatus } from '@/lib/ntpStatus'
import ClockCard from '@/views/services/time/ClockCard.vue'
import ServeCard from '@/views/services/time/ServeCard.vue'
import ServersCard from '@/views/services/time/ServersCard.vue'

const { status, state } = useNtpStatus({ poll: true })
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <template #status><StatusBadge v-if="state" :state="state" /></template>
    </PageHeader>
    <template v-if="status">
      <AppNotice v-if="!status.setUp">
        The time service is not set up on this router yet, so the distribution keeps the clock. Run
        <code class="font-mono">ostiole repair</code> as root once.
      </AppNotice>
      <AppNotice v-else-if="status.hostClock" kind="info">
        The host keeps this router's clock.
      </AppNotice>
    </template>
    <ClockCard />
    <ServersCard />
    <ServeCard />
  </div>
</template>
