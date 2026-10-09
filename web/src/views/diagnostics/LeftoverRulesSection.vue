<script setup>
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, errorMessage, useAsync } from '@/lib/async'
import { adminOnly, useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

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
const legacy = computed(() => report.value?.legacy?.tables ?? [])
const sweepable = computed(() => legacy.value.filter((t) => !t.owner))

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
  <SectionCard
    title="Leftover rules"
    :count="legacy.length"
    intro="Rules an older firewall left in the kernel. They still filter traffic, and Ostiole
      never clears anybody else's rules on its own."
    flush
  >
    <template #actions>
      <RefreshButton
        :busy="refresh.busy.value"
        :updated-at="refresh.updatedAt.value"
        @click="refresh.run"
      />
      <ConfirmButton
        v-if="root && sweepable.length"
        label="Clear leftovers"
        :disabled="!auth.isAdmin"
        :title="auth.isAdmin ? undefined : adminOnly('clear', 'them')"
        :question="`Clear ${sweepable.length} leftover ruleset${sweepable.length === 1 ? '' : 's'}?`"
        description="Legacy tables are emptied and set to accept. The others are deleted. Ostiole's own table is not touched."
        confirm-label="Clear"
        @confirm="flush([])"
      />
    </template>
    <div v-if="actionError || refresh.error.value || output || busy" class="card-strip space-y-3">
      <ErrorLine v-if="actionError">{{ actionError }}</ErrorLine>
      <ErrorLine v-else-if="refresh.error.value">
        {{ refresh.error.value }}
      </ErrorLine>
      <pre
        v-if="output"
        class="max-h-64 overflow-auto rounded bg-page p-2 font-mono text-code whitespace-pre-wrap text-ink-2"
        >{{ output }}</pre>
      <p v-if="busy" role="status" class="text-ink-muted">Working.</p>
    </div>
    <table class="table">
      <thead>
        <tr>
          <th>Ruleset</th>
          <th class="num">Rules</th>
          <th>Chains</th>
          <th class="w-0"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!legacy.length">
          <td colspan="4" class="text-ink-muted">
            {{ emptyText(refresh, 'Nothing was left behind.') }}
          </td>
        </tr>
        <tr v-for="t in legacy" :key="t.backend + (t.family ?? '') + t.name">
          <td class="font-mono">
            {{ t.backend === 'legacy' ? 'legacy ' : '' }}{{ t.family }} {{ t.name }}
          </td>
          <td class="num">{{ t.rules || 0 }}</td>
          <td class="font-mono text-code">
            {{ (t.chains ?? []).join(' ') || '—' }}
            <p v-if="t.owner" class="font-sans text-sm text-ink-muted">
              Belongs to {{ t.owner }}, so it is left alone.
            </p>
          </td>
          <td class="actions">
            <ConfirmButton
              v-if="root && t.owner"
              label="Clear anyway"
              :disabled="!auth.isAdmin"
              :title="auth.isAdmin ? undefined : adminOnly('clear', 'it')"
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
</template>
