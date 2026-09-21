<script setup>
import { computed, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'

/**
 * The router Ostiole runs on, as facts: the distribution, the units, the
 * daemons it drives, who owns the addresses. The only thing to press is
 * the one that clears what an older firewall left in the kernel, because
 * it is the only one the daemon can do from inside its sandbox. Packages
 * and units are `ostiole repair` on the console.
 */

const report = ref(null)
const actionError = ref('')
const output = ref('')
const busy = ref(false)

const refresh = useAsync(
  async () => {
    report.value = await api.host.status()
  },
  { immediate: true },
)

const root = computed(() => report.value?.root === true)
const units = computed(() => report.value?.units ?? [])
const network = computed(() => report.value?.network ?? {})
const legacy = computed(() => report.value?.legacy?.tables ?? [])
const sweepable = computed(() => legacy.value.filter((t) => !t.owner))
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

/** @param {string[]} tables */
async function flush(tables) {
  busy.value = true
  actionError.value = ''
  output.value = ''
  try {
    const result = await api.host.flushLegacy(tables)
    output.value = result.output ?? ''
    if (result.status) report.value = result.status
  } catch (e) {
    actionError.value = errorMessage(e)
    await refresh.run()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="space-y-5">
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
        <dl class="kv max-w-xl">
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
              network.owned
                ? 'owned by Ostiole'
                : 'owned by ' + (network.managers?.join(', ') || '—')
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

        <p v-if="actionError" role="alert" class="text-bad">{{ actionError }}</p>
        <p v-else-if="refresh.error.value" role="alert" class="text-bad">
          {{ refresh.error.value }}
        </p>
        <pre
          v-if="output"
          class="max-h-64 overflow-auto rounded bg-page p-2 font-mono text-code whitespace-pre-wrap text-ink-2"
          >{{ output }}</pre>
        <p v-if="busy" role="status" class="text-ink-muted">Working.</p>
      </div>
    </SectionCard>

    <SectionCard
      title="Leftover rules"
      :count="legacy.length"
      intro="Rules an older firewall left in the kernel. They still filter traffic, and Ostiole
        never clears anybody else's rules on its own."
      flush
    >
      <template v-if="root && sweepable.length" #actions>
        <ConfirmButton
          label="Clear leftovers"
          :question="`Clear ${sweepable.length} leftover ruleset${sweepable.length === 1 ? '' : 's'}?`"
          description="Legacy tables are emptied and their policies set to accept; nf_tables leftovers are deleted. Ostiole's own table is not touched."
          confirm-label="Clear"
          @confirm="flush([])"
        />
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Ruleset</th>
            <th>Rules</th>
            <th>Chains</th>
            <th class="w-0"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!legacy.length">
            <td colspan="4" class="text-ink-muted">Nothing was left behind.</td>
          </tr>
          <tr v-for="t in legacy" :key="t.backend + (t.family ?? '') + t.name">
            <td class="font-mono">
              {{ t.backend === 'legacy' ? 'legacy ' : '' }}{{ t.family }} {{ t.name }}
            </td>
            <td>{{ t.rules || 0 }}</td>
            <td class="font-mono text-code">
              {{ (t.chains ?? []).join(' ') || '—' }}
              <p v-if="t.owner" class="font-sans text-sm text-ink-muted">
                Belongs to {{ t.owner }}, so it is left alone.
              </p>
            </td>
            <td>
              <ConfirmButton
                v-if="root && t.owner"
                label="Clear anyway"
                :question="`Clear ${t.family} ${t.name}?`"
                :description="`This ruleset belongs to ${t.owner}. Clearing it breaks whatever is using it until that is restarted.`"
                confirm-label="Clear"
                :typed="t.name"
                @confirm="
                  flush([(t.backend === 'legacy' ? 'legacy ' : '') + t.family + ' ' + t.name])
                "
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
