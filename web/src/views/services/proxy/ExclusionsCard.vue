<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { exclusionText, firstRule, sameExclusion } from '@/lib/exclusions'
import { useSearch } from '@/lib/search'
import { byNumber, byText, useSort } from '@/lib/sort'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ExclusionDialog from '@/views/services/proxy/ExclusionDialog.vue'

const COLUMNS = [
  ['rule', 'Rule'],
  ['path', 'Path'],
  ['target', 'Variable'],
]

const auth = useAuthStore()
const config = useConfigStore()

const profiles = computed(() => config.proxy.wafProfiles ?? [])
const chosen = ref('')
/** The profile shown: the one picked, or the first once that one is gone. */
const profile = computed(
  () => profiles.value.find((w) => w.id === chosen.value) ?? profiles.value[0],
)
const picked = computed({
  get: () => profile.value?.id ?? '',
  set: (id) => (chosen.value = id),
})

/** Each exclusion with its place in the profile's list, which edits go by. */
const list = computed(() => (profile.value?.exclusions ?? []).map((e, index) => ({ ...e, index })))

const { query, shown } = useSearch(list, (e) => ({
  values: [e.rule, e.path, e.target, e.description],
}))

// Rule order to start with, so one rule's paths sit together.
const sort = useSort(
  shown,
  {
    rule: byNumber(firstRule, 'asc'),
    path: byText((e) => e.path),
    target: byText((e) => e.target),
  },
  { by: 'rule', tie: 'path' },
)
const rows = sort.sorted

const empty = computed(() =>
  list.value.length ? `Nothing matches "${query.value.trim()}".` : 'No exclusions.',
)

/** The profile's exclusions as the router has them. */
const saved = computed(
  () =>
    (config.saved?.services?.proxy?.wafProfiles ?? []).find((w) => w.id === profile.value?.id)
      ?.exclusions ?? [],
)
const changed = (e) =>
  !saved.value.some((s) => sameExclusion(s, e) && (s.description ?? '') === (e.description ?? ''))

/** A path in pieces that each start at a slash, where it may wrap. */
const pieces = (path) => path.split(/(?=\/)/)

const editing = ref(null)
const open = ref(false)
function add() {
  editing.value = null
  open.value = true
}
function edit(e) {
  editing.value = e
  open.value = true
}
</script>

<template>
  <SectionCard title="Exclusions" :count="list.length" flush>
    <template #actions>
      <SortSelect :sort="sort" :columns="COLUMNS" />
      <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="add">
        <Plus class="size-4" aria-hidden="true" /> Add exclusion
      </button>
    </template>
    <div class="card-strip-row">
      <select
        v-if="profiles.length > 1"
        v-model="picked"
        class="input w-48 font-mono max-sm:w-full"
        aria-label="WAF profile"
      >
        <option v-for="w in profiles" :key="w.id" :value="w.id">{{ w.id }}</option>
      </select>
      <SearchBox
        v-model="query"
        placeholder="rule, path, variable, or description"
        :shown="shown.length"
        :total="list.length"
      />
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <SortHeader by="rule" :sort="sort">Rule</SortHeader>
          <SortHeader by="path" :sort="sort">Path</SortHeader>
          <SortHeader by="target" :sort="sort">Variable</SortHeader>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!rows.length">
          <td colspan="4" class="text-ink-muted">{{ empty }}</td>
        </tr>
        <tr v-for="e in rows" :key="e.index" :class="{ 'row-changed': changed(e) }">
          <td data-label="">
            <div class="font-mono font-medium">{{ e.rule }}</div>
            <div v-if="e.description" class="text-xs text-ink-muted">{{ e.description }}</div>
          </td>
          <td data-label="Path">
            <span v-if="e.path" class="font-mono break-words text-code"
              ><template v-for="(piece, i) in pieces(e.path)" :key="i"
                ><wbr v-if="i" />{{ piece }}</template
              ></span
            >
            <span v-else class="text-ink-muted">every path</span>
          </td>
          <td data-label="Variable">
            <span v-if="e.target" class="font-mono break-words text-code">{{ e.target }}</span>
            <span v-else class="text-ink-muted">every variable</span>
          </td>
          <td class="actions" data-label="">
            <button type="button" class="link-action" @click="edit(e)">
              {{ auth.readOnly ? 'View' : 'Edit' }}
            </button>
            <ConfirmButton
              label="Delete"
              :question="`Delete exclusion ${exclusionText(e)}?`"
              :description="e.description"
              @confirm="config.removeExclusion(profile.id, e.index)"
            />
          </td>
        </tr>
      </tbody>
    </table>
  </SectionCard>

  <ExclusionDialog
    v-if="profile"
    v-model:open="open"
    :profile="profile.id"
    :exclusion="editing"
    :index="editing?.index ?? -1"
  />
</template>
