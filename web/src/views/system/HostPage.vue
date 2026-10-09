<script setup>
import { computed, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'

/**
 * The router Ostiole runs on, as facts: the distribution, the units, the
 * daemons it drives, who owns the addresses. Packages and units are
 * `ostiole repair` on the console.
 */

const report = ref(null)

const refresh = useAsync(
  async () => {
    report.value = await api.host.status()
  },
  { immediate: true },
)

const root = computed(() => report.value?.root === true)
const units = computed(() => report.value?.units ?? [])
const network = computed(() => report.value?.network ?? {})
const present = computed(() => {
  const p = report.value?.present ?? {}
  return Object.keys(p)
    .filter((name) => p[name])
    .join(' ')
})
const missing = computed(() => {
  const p = report.value?.present ?? {}
  return Object.keys(p)
    .filter((name) => !p[name])
    .join(' ')
})

/** @param {{active?: string, enabled?: string}} u */
function unitClass(u) {
  return u.active === 'active' || u.enabled === 'enabled' ? 'badge badge-ok' : 'badge badge-warn'
}
</script>

<template>
  <SectionCard title="This router">
    <template #intro>
      What Ostiole found underneath itself. To put the packages and units back, run
      <code class="font-mono text-code">ostiole repair</code> on the console.
    </template>
    <template #actions>
      <RefreshButton
        :busy="refresh.busy.value"
        :updated-at="refresh.updatedAt.value"
        @click="refresh.run"
      />
    </template>
    <div class="space-y-4">
      <p v-if="!report" class="text-ink-muted">Reading…</p>
      <dl v-else class="kv">
        <dt>Distribution</dt>
        <dd>{{ report?.distro || 'unknown' }}</dd>
        <dt>Package manager</dt>
        <dd class="font-mono">{{ report?.manager || 'none found' }}</dd>
        <dt>Kernel</dt>
        <dd class="font-mono">{{ report?.kernel || 'unknown' }}</dd>
        <template v-for="u in units" :key="u.name">
          <dt>{{ u.name }}</dt>
          <dd>
            <span :class="unitClass(u)">{{ u.active }}, {{ u.enabled }}</span>
          </dd>
        </template>
        <dt>Addresses</dt>
        <dd>
          {{
            network.owned ? 'owned by Ostiole' : 'owned by ' + (network.managers?.join(', ') || '—')
          }}
        </dd>
        <dt>Bluetooth</dt>
        <dd>
          <span v-if="report?.bluetooth === 'loaded'" class="text-warn">
            loaded. Run <code class="font-mono text-code">ostiole repair</code> as root.
          </span>
          <span v-else>{{ report?.bluetooth === 'blocked' ? 'blocked' : 'none' }}</span>
        </dd>
        <dt>Services present</dt>
        <dd class="font-mono">{{ present || 'none' }}</dd>
        <dt v-if="missing">Missing</dt>
        <dd v-if="missing" class="font-mono">{{ missing }}</dd>
      </dl>

      <AppNotice v-if="report && !root">
        This daemon is not running as root, so it reports what it found and changes nothing.
      </AppNotice>
      <AppNotice v-if="network.pending">
        A handover is waiting. On the console:
        <code class="font-mono text-code">ostiole takeover --network --confirm</code> keeps it, or
        the previous manager comes back on its own.
      </AppNotice>

      <ErrorLine v-if="refresh.error.value">{{ refresh.error.value }}</ErrorLine>
    </div>
  </SectionCard>
</template>
