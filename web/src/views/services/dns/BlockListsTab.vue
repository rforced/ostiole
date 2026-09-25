<script setup>
import { LoaderCircle, Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import AppDisclosure from '@/components/AppDisclosure.vue'
import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatCount } from '@/lib/format'
import { byNumber, byText, byTime, useSort } from '@/lib/sort'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import BlockListDialog from '@/views/services/dns/BlockListDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const blocking = computed(() => config.ensureBlocking())
const dnsOn = computed(() => Boolean(config.draft?.services?.dns?.enabled))

/** NXDOMAIN is the default, and the default is left out of the model. */
const mode = computed({
  get: () => blocking.value.mode ?? 'nxdomain',
  set: (v) => {
    if (v === 'nxdomain') delete blocking.value.mode
    else blocking.value.mode = v
  },
})

const editing = ref(null)
const open = ref(false)
const status = ref(null)
/** Which refresh is in flight: 'all', a list name, or nothing. */
const which = ref('')
const note = ref('')

/** What the router has actually fetched, keyed by list name. */
const fetched = computed(() =>
  Object.fromEntries((status.value?.lists ?? []).map((s) => [s.name, s])),
)

/**
 * The lists the router knows about. A list added to the draft and not applied
 * yet is not one of them, and refreshing it would only report that it does
 * not exist, so the row says so instead of offering the button.
 */
const applied = computed(() => new Set((status.value?.lists ?? []).map((s) => s.name)))
const limits = computed(() => status.value?.limits ?? {})
/** What each list has actually refused; empty while the query log is off. */
const counts = computed(() => status.value?.counts ?? {})
const ceiling = computed(() => limits.value.maxDomains ?? 1000000)
/** What the lists come to added together, before merging. */
const total = computed(() => status.value?.totals?.domains ?? 0)
/** What the last installed merge came to: the number that costs memory. */
const blocked = computed(() => status.value?.totals?.blocked ?? 0)
const memoryMB = computed(() => status.value?.totals?.estimatedMemoryMb ?? 0)

const COLUMNS = [
  ['name', 'List'],
  ['names', 'Names'],
  ['blocked', 'Blocked'],
  ['source', 'Source'],
  ['fetched', 'Fetched'],
]

/**
 * Whether the ceiling is in the way. The merged number is the one that
 * counts; before a first merge there is only the sum of the lists, which
 * overstates it by however much they overlap.
 */
const overCeiling = computed(() => (blocked.value || total.value) > ceiling.value)

/** The ceiling, with the default left out of the model when unchanged. */
const maxDomains = computed({
  get: () => blocking.value.maxDomains ?? limits.value.defaultMax ?? 1000000,
  set: (v) => {
    const n = Number(v)
    if (!n || n === limits.value.defaultMax) delete blocking.value.maxDomains
    else blocking.value.maxDomains = n
  },
})

/** What a ceiling of n names would cost in memory, in whole MB. */
function costMB(n) {
  return Math.round((n * (limits.value.bytesPerName ?? 90)) / (1024 * 1024))
}

const load = useAsync(async () => {
  status.value = await api.blocking.status()
})
onMounted(load.run)

const refresh = useAsync(async (name) => {
  which.value = name || 'all'
  note.value = ''
  try {
    if (name) {
      await api.blocking.refresh(name)
      await load.run()
    } else {
      // The refresh answers with the state it left behind, and a report of
      // what it did, so a pass that fetched nothing can say why.
      status.value = await api.blocking.refreshAll()
      note.value = describe(status.value?.report)
    }
  } finally {
    which.value = ''
  }
})

/** Turns a refresh report into the sentence to show under the buttons. */
function describe(report) {
  if (!report) return ''
  if (report.note) return report.note
  const parts = []
  if (report.fetched?.length) parts.push(`${report.fetched.length} updated`)
  if (report.unchanged?.length) parts.push(`${report.unchanged.length} unchanged`)
  const failed = Object.keys(report.failed ?? {})
  if (failed.length) parts.push(`${failed.length} failed`)
  if (!parts.length) return 'Nothing to fetch.'
  return `Refreshed: ${parts.join(', ')}.`
}

function add() {
  editing.value = null
  open.value = true
}
function edit(l) {
  editing.value = l
  open.value = true
}

/** Where a list's names come from, for the table. */
function source(l) {
  if (l.url) return l.url
  return 'loaded by hand'
}

function when(s) {
  if (!s?.fetchedAt) return 'never'
  return new Date(s.fetchedAt).toLocaleString()
}

/** In the order they were added until a header says otherwise. */
const sort = useSort(() => config.blockLists, {
  name: byText((l) => l.name),
  names: byNumber((l) => fetched.value[l.name]?.domains),
  blocked: byNumber((l) => counts.value[l.name]?.blocked),
  source: byText(source),
  fetched: byTime((l) => fetched.value[l.name]?.fetchedAt),
})
const lists = sort.sorted
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Block lists" :locked="auth.readOnly">
      <template #actions>
        <ToggleRow
          v-model="blocking.enabled"
          variant="switch"
          label="Enabled"
          aria-label="Block lists enabled"
          :disabled="auth.readOnly"
        />
      </template>
      <div class="space-y-4">
        <AppNotice v-if="blocking.enabled && !dnsOn">
          The DNS server is off, so nothing is refused. Turn it on under Resolver.
        </AppNotice>
        <FormField
          id="block-mode"
          label="Blocked names are answered with"
          hint="NXDOMAIN fails immediately. A null address is easier to spot in a capture."
          class="max-w-md"
        >
          <select id="block-mode" v-model="mode" class="input">
            <option value="nxdomain">NXDOMAIN, no such name</option>
            <option value="null">0.0.0.0 and ::, a null address</option>
          </select>
        </FormField>
        <AppDisclosure>
          <FormField
            id="block-max"
            label="Ceiling on merged names"
            :hint="`About ${costMB(maxDomains)} MB of memory at that many. Default ${formatCount(limits.defaultMax ?? 1000000)}.`"
            class="max-w-md"
          >
            <input
              id="block-max"
              v-model.number="maxDomains"
              type="number"
              min="1000"
              :max="limits.hardMax ?? 25000000"
              step="50000"
              class="input w-40 font-mono max-sm:w-full"
            />
          </FormField>
          <p class="max-w-2xl text-ink-muted">
            Overlapping lists merge down a long way. Raise this no further than the memory on this
            router goes.
          </p>
        </AppDisclosure>
      </div>
    </SectionCard>

    <SectionCard
      title="Lists"
      :count="config.blockLists.length"
      intro="A name on a list blocks everything under it. Names to let back through go under Exceptions."
      flush
    >
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <template v-if="!auth.readOnly">
          <RefreshButton
            v-if="config.blockLists.some((l) => l.url)"
            :busy="refresh.busy.value"
            :updated-at="refresh.updatedAt.value"
            label="Refresh lists"
            @click="refresh.run('')"
          />
          <button type="button" class="btn-secondary" :disabled="refresh.busy.value" @click="add">
            <Plus class="size-4" aria-hidden="true" /> Add list
          </button>
        </template>
      </template>
      <div class="card-strip space-y-1 empty:hidden">
        <p v-if="total" class="text-ink-muted">
          {{ formatCount(total) }} names across the lists that are on.
          <template v-if="blocked">
            {{ formatCount(blocked) }} after merging, about {{ memoryMB }} MB of memory.
          </template>
          <template v-else>Nothing has been merged yet.</template>
          <span v-if="overCeiling" class="badge badge-warn ml-1">
            over the ceiling of {{ formatCount(ceiling) }}
          </span>
        </p>
        <p v-if="refresh.error.value || load.error.value" role="alert" class="text-bad">
          {{ refresh.error.value || load.error.value }}
        </p>
        <p v-else-if="which === 'all'" aria-live="polite" class="text-ink-muted">
          Fetching every list. Nothing is installed until they are all in.
        </p>
        <p v-else-if="note" aria-live="polite" class="text-ink-muted">{{ note }}</p>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <SortHeader by="name" :sort="sort">List</SortHeader>
            <th>On</th>
            <SortHeader by="names" :sort="sort">Names</SortHeader>
            <SortHeader by="blocked" :sort="sort" title="Counts need the query log.">
              Blocked
            </SortHeader>
            <SortHeader by="source" :sort="sort">Source</SortHeader>
            <SortHeader by="fetched" :sort="sort">Fetched</SortHeader>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!config.blockLists.length">
            <td colspan="7" class="text-ink-muted">
              No lists. "Add list" offers the well-known ones.
            </td>
          </tr>
          <tr
            v-for="l in lists"
            :key="l.name"
            :class="{ 'row-changed': config.isChanged('blocking.lists', l.name) }"
          >
            <td class="font-mono font-medium" data-label="">
              {{ l.name }}
              <div v-if="l.description" class="font-sans text-sm font-normal text-ink-muted">
                {{ l.description }}
              </div>
            </td>
            <td data-label="On">
              <label
                class="max-sm:-my-3 max-sm:inline-flex max-sm:min-h-11 max-sm:min-w-11 max-sm:items-center"
              >
                <input
                  type="checkbox"
                  class="size-4 rounded"
                  :checked="l.enabled"
                  :disabled="auth.readOnly"
                  :aria-label="`${l.name} enabled`"
                  @change="config.upsertBlockList({ ...l, enabled: $event.target.checked })"
                />
              </label>
            </td>
            <td class="font-mono text-code" data-label="Names">
              <template v-if="fetched[l.name]?.fetchedAt">
                {{ formatCount(fetched[l.name].domains) }}
                <div v-if="fetched[l.name].skipped" class="text-ink-muted">
                  {{ formatCount(fetched[l.name].skipped) }}
                  {{ fetched[l.name].skipped === 1 ? 'line' : 'lines' }} skipped
                </div>
              </template>
              <span v-else-if="!applied.has(l.name)" class="text-ink-muted"> not applied yet </span>
              <span v-else class="text-ink-muted">not fetched yet</span>
            </td>
            <td class="text-code" data-label="Blocked">
              <template v-if="counts[l.name]">
                {{ formatCount(counts[l.name].blocked) }}
                <div v-if="counts[l.name].alone" class="text-xs text-ink-muted">
                  {{ formatCount(counts[l.name].alone) }} only here
                </div>
              </template>
              <span v-else class="text-ink-muted">&mdash;</span>
            </td>
            <td class="max-w-xs max-sm:max-w-none" data-label="Source">
              <div class="font-mono text-code break-all text-ink-muted">{{ source(l) }}</div>
              <div v-if="fetched[l.name]?.format" class="text-xs text-ink-muted">
                read as {{ fetched[l.name].format }}
              </div>
            </td>
            <td class="text-code" data-label="Fetched">
              <span v-if="fetched[l.name]?.lastError" class="text-sm text-bad">
                {{ fetched[l.name].lastError }}
              </span>
              <template v-else>
                {{ when(fetched[l.name]) }}
                <span v-if="fetched[l.name]?.stale" class="badge badge-warn ml-1">stale</span>
              </template>
            </td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button
                v-if="l.url && applied.has(l.name) && !auth.readOnly"
                type="button"
                class="link mr-3"
                :disabled="refresh.busy.value"
                :aria-busy="which === l.name"
                @click="refresh.run(l.name)"
              >
                <LoaderCircle
                  v-if="which === l.name"
                  class="mr-1 inline size-4 animate-spin"
                  aria-hidden="true"
                />
                {{ which === l.name ? 'Refreshing…' : 'Refresh' }}
              </button>
              <button type="button" class="link" :disabled="refresh.busy.value" @click="edit(l)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                :disabled="refresh.busy.value"
                label="Delete"
                :question="`Delete list ${l.name}?`"
                :description="l.description"
                @confirm="config.removeBlockList(l.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <BlockListDialog v-model:open="open" :list="editing" />
  </div>
</template>
