<script setup>
import { computed } from 'vue'
import { RouterLink } from 'vue-router'

import SectionCard from '@/components/SectionCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatCount, formatDuration, formatOffset } from '@/lib/format'
import { useNtpStatus } from '@/lib/ntpStatus'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const { status } = useNtpStatus()

/** The zone the router runs in, which is set under System, General. */
const zone = computed(() => config.saved?.system?.timezone || 'UTC')

/** How long ago the clock was last set from a server, as of the last read. */
const lastSet = computed(() => {
  const at = Date.parse(status.value?.lastUpdate ?? '')
  if (!Number.isFinite(at)) return ''
  const seconds = Math.max(0, Math.round((Date.now() - at) / 1000))
  return seconds < 60 ? `${seconds}s ago` : `${formatDuration(seconds)} ago`
})

/** A card only while there is something to say about the service. */
const shown = computed(() => !status.value || (status.value.setUp && !status.value.hostClock))
</script>

<template>
  <SectionCard v-if="shown" title="Clock">
    <p v-if="!status" class="text-ink-muted">Reading…</p>
    <p v-else-if="!status.running" class="text-ink-muted">Stopped. Applying starts it again.</p>
    <p v-else-if="!status.read" class="text-ink-muted">Not answering.</p>
    <dl v-else class="kv">
      <dt>State</dt>
      <dd>
        <StatusBadge :state="status.synchronised ? 'synchronised' : 'not synchronised'" />
      </dd>
      <template v-if="status.reference">
        <dt>Following</dt>
        <dd class="font-mono">{{ status.reference }}</dd>
      </template>
      <template v-if="status.synchronised">
        <dt>Offset</dt>
        <dd>{{ formatOffset(status.offsetSeconds) }}</dd>
        <dt>Stratum</dt>
        <dd>{{ status.stratum }}</dd>
      </template>
      <template v-if="lastSet">
        <dt>Last set</dt>
        <dd>{{ lastSet }}</dd>
      </template>
      <template v-if="status.served">
        <dt>Requests answered</dt>
        <dd>{{ formatCount(status.served.packets) }}</dd>
      </template>
      <dt>Timezone</dt>
      <dd>
        <RouterLink to="/system/general" class="link">{{ zone }}</RouterLink>
      </dd>
    </dl>
  </SectionCard>
</template>
