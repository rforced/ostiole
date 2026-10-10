<script setup>
import { computed, onMounted, ref, watch } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ChangeList from '@/components/ChangeList.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, errorMessage, useAsync } from '@/lib/async'
import { actorText } from '@/lib/audit'
import { formatWhen } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const revisions = ref([])
const actionError = ref('')
const loadedId = ref('')
const comparing = ref('')
const changes = ref([])
const inForce = ref(null)
const inForceError = ref('')
const columns = computed(() => (auth.isAdmin ? 5 : 4))

/** Matches model.DefaultKeepRevisions, which is what an unset setting means. */
const DEFAULT_KEEP = 20
const MAX_KEEP = 1000

/**
 * The history bound. It is edited locally rather than straight into the
 * draft so that emptying the field stays empty: a computed that fell back to
 * the default would refill it under the cursor. The configuration leaves
 * the setting out when it is the default, so an empty field means the same.
 */
const keep = ref(DEFAULT_KEEP)

// Seeded from the draft, and again when a revision or a restored backup
// replaces it wholesale.
watch(
  () => config.draft,
  (d) => (keep.value = d?.system?.keepRevisions || DEFAULT_KEEP),
  {
    immediate: true,
  },
)

watch(keep, (v) => {
  const n = Number(v)
  if (!n || n === DEFAULT_KEEP) delete config.draft.system.keepRevisions
  else config.draft.system.keepRevisions = n
})

/** Who applied the configuration in force. Admins only; a failure leaves the table alone. */
async function readInForce() {
  try {
    inForce.value = await api.config.applied()
    inForceError.value = ''
  } catch (e) {
    inForce.value = null
    inForceError.value = errorMessage(e)
  }
}

const load = useAsync(async () => {
  const reading = auth.isAdmin ? readInForce() : null
  revisions.value = await api.config.revisions()
  await reading
})

/** The line in the card strip for the configuration in force. */
function inForceLine(a) {
  if (a.outside) return `Changed outside Ostiole on ${formatWhen(a.time)}.`
  const from = a.by?.address ? ` from ${a.by.address}` : ''
  const confirmed = a.confirmedBy ? `, confirmed by ${actorText(a.confirmedBy)}` : ''
  return `In force since ${formatWhen(a.time)}, applied by ${actorText(a.by)}${from}${confirmed}.`
}

/** The second line under who applied a revision. */
function appliedDetail(a) {
  return [
    formatWhen(a.time),
    a.by?.address,
    a.confirmedBy && `confirmed by ${actorText(a.confirmedBy)}`,
  ]
    .filter(Boolean)
    .join(' · ')
}

onMounted(load.run)

async function loadIntoDraft(id) {
  if (
    config.dirty &&
    !(await confirm.ask({
      question: 'Replace the current draft?',
      description: 'Its unapplied changes are lost.',
      confirmLabel: 'Replace',
    }))
  )
    return
  actionError.value = ''
  try {
    config.replaceDraft(await api.config.revision(id))
    loadedId.value = id
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

/** Shows what changed between a revision and the configuration in force. */
async function compare(id) {
  actionError.value = ''
  if (comparing.value === id) {
    comparing.value = ''
    return
  }
  try {
    changes.value = await api.config.diff({ from: id, to: 'current' })
    comparing.value = id
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

defineExpose({ refresh: load.run })
</script>

<template>
  <SectionCard
    title="Revisions"
    :count="revisions.length"
    intro="Every confirmed apply archives the configuration it replaced."
    flush
  >
    <template #actions>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </template>
    <div class="card-strip space-y-3">
      <FormField
        id="rev-keep"
        label="Revisions to keep"
        hint="The oldest is deleted once there are more than this."
      >
        <input
          id="rev-keep"
          v-model.number="keep"
          type="number"
          min="1"
          :max="MAX_KEEP"
          class="input w-32 max-sm:w-full"
          :disabled="auth.readOnly"
        />
      </FormField>
      <p v-if="auth.isAdmin && inForce" class="text-sm">
        {{ inForceLine(inForce) }}
      </p>
      <ErrorLine v-if="actionError || load.error.value || inForceError">
        {{ actionError || load.error.value || inForceError }}
      </ErrorLine>
      <AppNotice v-if="loadedId" role="status">
        Revision <span class="font-mono">{{ loadedId }}</span> is now the draft. Apply it to roll
        back, or discard.
      </AppNotice>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Archived</th>
          <th>ID</th>
          <th class="num">Size</th>
          <th v-if="auth.isAdmin">Applied by</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="revisions.length === 0">
          <td :colspan="columns" class="text-ink-muted">
            {{ emptyText(load, 'No revisions.') }}
          </td>
        </tr>
        <template v-for="r in revisions" :key="r.id">
          <tr>
            <td data-label="">{{ formatWhen(r.time) }}</td>
            <td class="font-mono text-code" data-label="ID">{{ r.id }}</td>
            <td class="num font-mono text-code" data-label="Size">{{ r.size }} B</td>
            <td v-if="auth.isAdmin" data-label="Applied by">
              <template v-if="r.applied?.outside">
                Changed outside Ostiole
                <div class="text-xs text-ink-muted">{{ formatWhen(r.applied.time) }}</div>
              </template>
              <template v-else-if="r.applied">
                {{ actorText(r.applied.by) }}
                <div class="text-xs text-ink-muted">{{ appliedDetail(r.applied) }}</div>
              </template>
              <span v-else class="text-ink-muted">not recorded</span>
            </td>
            <td class="actions" data-label="">
              <button
                type="button"
                class="link-action"
                :aria-expanded="comparing === r.id"
                @click="compare(r.id)"
              >
                {{ comparing === r.id ? 'Hide changes' : 'Compare with current' }}
              </button>
              <button
                v-if="!auth.readOnly"
                type="button"
                class="link-action"
                @click="loadIntoDraft(r.id)"
              >
                Load into draft
              </button>
            </td>
          </tr>
          <!-- A key of its own: in the transition group both rows of a
               revision would share one, and opening another compare
               patched one row onto the other. -->
          <tr v-if="comparing === r.id">
            <td :colspan="columns" class="bg-surface-2/40">
              <ChangeList
                :changes="changes"
                empty-label="Nothing changed between this revision and the current configuration."
              />
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </SectionCard>
</template>
