<script setup>
import { onMounted, ref, watch } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ChangeList from '@/components/ChangeList.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
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

const load = useAsync(async () => {
  revisions.value = await api.config.revisions()
})
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
    title="Configuration history"
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
        label="Configurations to keep"
        hint="The oldest is deleted once there are more than this."
      >
        <input
          id="rev-keep"
          v-model.number="keep"
          type="number"
          min="1"
          :max="MAX_KEEP"
          class="input w-32"
          :disabled="auth.readOnly"
        />
      </FormField>
      <p v-if="actionError || load.error.value" role="alert" class="text-bad">
        {{ actionError || load.error.value }}
      </p>
      <AppNotice v-if="loadedId" role="status">
        Revision <span class="font-mono">{{ loadedId }}</span> is now the draft. Apply it to roll
        back, or discard.
      </AppNotice>
    </div>
    <table class="table">
      <thead>
        <tr>
          <th>Archived</th>
          <th>ID</th>
          <th>Size</th>
          <th></th>
        </tr>
      </thead>
      <TransitionGroup name="row" tag="tbody">
        <tr v-if="revisions.length === 0" key="empty" class="row-static">
          <td colspan="4" class="text-ink-muted">
            {{ load.updatedAt.value ? 'No revisions.' : 'Reading…' }}
          </td>
        </tr>
        <template v-for="r in revisions" :key="r.id">
          <tr>
            <td>{{ new Date(r.time).toLocaleString() }}</td>
            <td class="font-mono text-code">{{ r.id }}</td>
            <td class="font-mono text-code">{{ r.size }} B</td>
            <td class="text-right whitespace-nowrap">
              <button
                type="button"
                class="link"
                :aria-expanded="comparing === r.id"
                @click="compare(r.id)"
              >
                {{ comparing === r.id ? 'Hide changes' : 'Compare with current' }}
              </button>
              <button
                v-if="!auth.readOnly"
                type="button"
                class="link ml-3"
                @click="loadIntoDraft(r.id)"
              >
                Load into draft
              </button>
            </td>
          </tr>
          <!-- A key of its own: in the transition group both rows of a
               revision would share one, and opening another compare
               patched one row onto the other. -->
          <tr v-if="comparing === r.id" :key="`${r.id}-changes`">
            <td colspan="4" class="bg-surface-2/40">
              <ChangeList
                :changes="changes"
                empty-label="Nothing changed between this revision and the current configuration."
              />
            </td>
          </tr>
        </template>
      </TransitionGroup>
    </table>
  </SectionCard>
</template>
