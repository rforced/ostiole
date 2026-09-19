<script setup>
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
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
  <section class="card space-y-4" aria-labelledby="host-title">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 id="host-title" class="card-title">This router</h2>
        <p class="max-w-3xl text-sm text-neutral-500">
          What Ostiole found underneath itself. To put the packages and units back, run
          <code class="font-mono text-code">ostiole repair</code> on the console.
        </p>
      </div>
      <RefreshButton
        :busy="refresh.busy.value"
        :updated-at="refresh.updatedAt.value"
        @click="refresh.run"
      />
    </div>

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
          network.owned ? 'owned by Ostiole' : 'owned by ' + (network.managers?.join(', ') || '—')
        }}
      </dd>
      <dt>Bluetooth</dt>
      <dd>
        <span v-if="report?.bluetooth === 'loaded'" class="text-amber-700 dark:text-amber-300">
          loaded. Run <code class="font-mono text-code">ostiole repair</code> as root.
        </span>
        <span v-else>{{ report?.bluetooth === 'blocked' ? 'blocked' : 'none' }}</span>
      </dd>
      <dt>Services present</dt>
      <dd class="font-mono">{{ present || 'none' }}</dd>
      <dt v-if="missing">Missing</dt>
      <dd v-if="missing" class="font-mono">{{ missing }}</dd>
    </dl>

    <p v-if="report && !root" class="text-sm text-amber-700 dark:text-amber-300">
      This daemon is not running as root, so it reports what it found and changes nothing.
    </p>
    <p v-if="network.pending" class="text-sm text-amber-700 dark:text-amber-300">
      A handover is waiting. On the console:
      <code class="font-mono text-code">ostiole takeover --network --confirm</code> keeps it, or the
      previous manager comes back on its own.
    </p>

    <!-- Leftover rules ----------------------------------------------------- -->
    <div class="flex flex-wrap items-start justify-between gap-3 pt-2">
      <div>
        <h3 class="font-medium">Leftover rules</h3>
        <p class="max-w-3xl text-sm text-neutral-500">
          Rules an older firewall left in the kernel. They still filter traffic, and Ostiole never
          clears anybody else's rules on its own.
        </p>
      </div>
      <ConfirmButton
        v-if="root && sweepable.length"
        label="Clear leftovers"
        :question="`Clear ${sweepable.length} leftover ruleset${sweepable.length === 1 ? '' : 's'}?`"
        description="Legacy tables are emptied and their policies set to accept; nf_tables leftovers are deleted. Ostiole's own table is not touched."
        confirm-label="Clear"
        @confirm="flush([])"
      />
    </div>

    <p v-if="!legacy.length" class="text-sm text-neutral-500">Nothing was left behind.</p>
    <div
      v-else
      class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800"
    >
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
          <tr v-for="t in legacy" :key="t.backend + (t.family ?? '') + t.name">
            <td class="font-mono">
              {{ t.backend === 'legacy' ? 'legacy ' : '' }}{{ t.family }} {{ t.name }}
            </td>
            <td>{{ t.rules || 0 }}</td>
            <td class="font-mono text-code">
              {{ (t.chains ?? []).join(' ') || '—' }}
              <p v-if="t.owner" class="font-sans text-sm text-neutral-500">
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
    </div>

    <p v-if="actionError" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ actionError }}
    </p>
    <p v-else-if="refresh.error.value" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ refresh.error.value }}
    </p>
    <pre
      v-if="output"
      class="max-h-64 overflow-auto rounded bg-neutral-50 p-2 font-mono text-code whitespace-pre-wrap text-neutral-700 dark:bg-neutral-900 dark:text-neutral-300"
      >{{ output }}</pre>
    <p v-if="busy" role="status" class="text-sm text-neutral-500">Working.</p>
  </section>
</template>
