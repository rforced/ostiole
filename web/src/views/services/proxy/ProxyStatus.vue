<script setup>
import { computed } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useProxyStatus } from '@/lib/proxyStatus'

/** The notices and the facts under the proxy's page header. It owns the read. */
const { status, stage, clash } = useProxyStatus({ poll: true })

const healthy = computed(() => (status.value?.upstreams ?? []).filter((u) => u.healthy).length)
</script>

<template>
  <div v-if="status">
    <AppNotice v-if="stage === 'missing'">
      Not on this router. Run <code class="font-mono">ostiole repair --proxy</code> as root once.
    </AppNotice>
    <AppNotice v-else-if="stage === 'port-clash'">
      Port {{ clash }} is the web UI's. Change it under System, General first.
    </AppNotice>
    <template v-else-if="stage === 'off'" />
    <p v-else-if="stage === 'unapplied'" class="text-sm text-ink-muted">
      Apply the draft to start it.
    </p>

    <SectionCard v-else title="Proxy">
      <dl v-if="stage === 'running'" class="kv">
        <template v-if="status.release">
          <dt>Release</dt>
          <dd class="font-mono">{{ status.release }}</dd>
        </template>
        <dt>Ports</dt>
        <dd class="font-mono">
          {{ status.ports.http }}, {{ status.ports.https
          }}<template v-if="status.ports.http3"> (+ UDP {{ status.ports.https }})</template>
        </dd>
        <dt>Upstreams</dt>
        <dd>{{ healthy }} of {{ status.upstreams.length }} healthy</dd>
      </dl>
      <p v-else class="text-ink-muted">Stopped.</p>
    </SectionCard>
  </div>
</template>
