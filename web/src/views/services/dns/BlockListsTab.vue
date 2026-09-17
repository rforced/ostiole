<script setup>
import { Plus, RefreshCw } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { formatCount } from '@/lib/format'
import { useConfigStore } from '@/stores/config'
import BlockListDialog from '@/views/services/dns/BlockListDialog.vue'

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
const busy = ref('')
const error = ref('')
const note = ref('')

/** What the box has actually fetched, keyed by list name. */
const fetched = computed(() =>
  Object.fromEntries((status.value?.lists ?? []).map((s) => [s.name, s])),
)

/**
 * The lists the box knows about. A list added to the draft and not applied
 * yet is not one of them, and refreshing it would only report that it does
 * not exist, so the row says so instead of offering the button.
 */
const applied = computed(() => new Set((status.value?.lists ?? []).map((s) => s.name)))
const limits = computed(() => status.value?.limits ?? {})
const ceiling = computed(() => limits.value.maxDomains ?? 1000000)
/** What the lists come to added together, before merging. */
const total = computed(() => status.value?.totals?.domains ?? 0)
/** What the last installed merge came to: the number that costs memory. */
const blocked = computed(() => status.value?.totals?.blocked ?? 0)
const memoryMB = computed(() => status.value?.totals?.estimatedMemoryMb ?? 0)

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

/** What a ceiling of n names would cost dnsmasq, in whole MB. */
function costMB(n) {
  return Math.round((n * (limits.value.bytesPerName ?? 90)) / (1024 * 1024))
}

onMounted(refreshStatus)

async function refreshStatus() {
  try {
    status.value = await api.blocking.status()
  } catch {
    status.value = null
  }
}

async function refreshNow(name) {
  if (busy.value) return
  error.value = ''
  note.value = ''
  busy.value = name || 'all'
  try {
    if (name) {
      await api.blocking.refresh(name)
      await refreshStatus()
    } else {
      // The refresh answers with the state it left behind, and a report of
      // what it did, so a pass that fetched nothing can say why.
      status.value = await api.blocking.refreshAll()
      note.value = describe(status.value?.report)
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = ''
  }
}

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
</script>

<template>
  <div class="space-y-4">
    <label class="flex items-center gap-2 text-sm">
      <input v-model="blocking.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
      <span class="font-medium">DNS blocking enabled</span>
      <span class="text-neutral-500">
        — this box refuses to resolve the names on the lists below.
      </span>
    </label>

    <p
      v-if="blocking.enabled && !dnsOn"
      role="note"
      class="max-w-2xl rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
    >
      The DNS server is off, so nothing is being refused. dnsmasq is what does the blocking; turn it
      on under Server.
    </p>

    <FormField
      id="block-mode"
      label="Blocked names are answered with"
      hint="NXDOMAIN fails immediately, which is what clients handle best. A null address is easier to spot in a capture."
      class="max-w-md"
    >
      <select id="block-mode" v-model="mode" class="input">
        <option value="nxdomain">NXDOMAIN — no such name</option>
        <option value="null">0.0.0.0 and :: — an address that goes nowhere</option>
      </select>
    </FormField>

    <div class="flex flex-wrap items-center gap-2">
      <button type="button" class="btn-secondary" :disabled="busy !== ''" @click="add">
        <Plus class="mr-1 size-4" aria-hidden="true" /> Add list
      </button>
      <button
        v-if="config.blockLists.some((l) => l.url)"
        type="button"
        class="btn-secondary"
        :disabled="busy !== ''"
        :aria-busy="busy === 'all'"
        @click="refreshNow('')"
      >
        <RefreshCw
          class="mr-1 size-4"
          :class="{ 'animate-spin': busy === 'all' }"
          aria-hidden="true"
        />
        {{ busy === 'all' ? 'Refreshing…' : 'Refresh lists' }}
      </button>
      <p v-if="total" class="text-sm text-neutral-500">
        {{ formatCount(total) }} names across the lists that are on.
        <template v-if="blocked">
          {{ formatCount(blocked) }} after merging, about {{ memoryMB }} MB in dnsmasq.
        </template>
        <template v-else>Nothing has been merged yet.</template>
        <span v-if="overCeiling" class="badge badge-warn ml-1">
          over the ceiling of {{ formatCount(ceiling) }}
        </span>
      </p>
    </div>

    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-else-if="busy === 'all'" aria-live="polite" class="text-sm text-neutral-500">
      Fetching every list. A big one takes a while, and nothing is installed until they are all in.
    </p>
    <p v-else-if="note" aria-live="polite" class="text-sm text-neutral-500">{{ note }}</p>

    <details :open="overCeiling" class="max-w-2xl text-sm">
      <summary class="cursor-pointer text-neutral-600 dark:text-neutral-400">
        How many names this box will hold
      </summary>
      <div class="mt-2 space-y-2">
        <FormField
          id="block-max"
          label="Ceiling on merged names"
          :hint="`About ${costMB(maxDomains)} MB of dnsmasq at that many. The default is ${formatCount(limits.defaultMax ?? 1000000)}; the most this box will take is ${formatCount(limits.hardMax ?? 5000000)}.`"
        >
          <input
            id="block-max"
            v-model.number="maxDomains"
            type="number"
            min="1000"
            :max="limits.hardMax ?? 5000000"
            step="50000"
            class="input w-40 font-mono"
          />
        </FormField>
        <p class="text-neutral-500">
          Lists that overlap merge down a long way; lists curated not to overlap, like HaGeZi's
          categories, barely move. Raise this only as far as the memory on this box allows — the
          ceiling is what turns "out of memory" into a message you can read.
        </p>
      </div>
    </details>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>List</th>
            <th>On</th>
            <th>Names</th>
            <th>Source</th>
            <th>Fetched</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!config.blockLists.length">
            <td colspan="6" class="text-neutral-500">
              No lists yet. "Add list" offers the well-known ones.
            </td>
          </tr>
          <tr v-for="l in config.blockLists" :key="l.name">
            <td class="font-mono font-medium">
              {{ l.name }}
              <div v-if="l.description" class="font-sans text-xs text-neutral-500">
                {{ l.description }}
              </div>
            </td>
            <td>
              <input
                type="checkbox"
                class="size-4 rounded border-neutral-300"
                :checked="l.enabled"
                :aria-label="`${l.name} enabled`"
                @change="config.upsertBlockList({ ...l, enabled: $event.target.checked })"
              />
            </td>
            <td class="font-mono text-xs">
              <template v-if="fetched[l.name]?.fetchedAt">
                {{ formatCount(fetched[l.name].domains) }}
                <div v-if="fetched[l.name].skipped" class="text-neutral-500">
                  {{ formatCount(fetched[l.name].skipped) }}
                  {{ fetched[l.name].skipped === 1 ? 'line' : 'lines' }} skipped
                </div>
              </template>
              <span v-else-if="!applied.has(l.name)" class="text-neutral-500">
                not applied yet
              </span>
              <span v-else class="text-neutral-500">not fetched yet</span>
            </td>
            <td class="max-w-xs">
              <div class="font-mono text-xs break-all text-neutral-500">{{ source(l) }}</div>
              <div v-if="fetched[l.name]?.format" class="text-xs text-neutral-500">
                read as {{ fetched[l.name].format }}
              </div>
            </td>
            <td class="text-xs">
              <span v-if="fetched[l.name]?.lastError" class="badge badge-warn">
                {{ fetched[l.name].lastError }}
              </span>
              <template v-else>
                {{ when(fetched[l.name]) }}
                <span v-if="fetched[l.name]?.stale" class="badge badge-warn ml-1">stale</span>
              </template>
            </td>
            <td class="text-right whitespace-nowrap">
              <button
                v-if="l.url && applied.has(l.name)"
                type="button"
                class="link mr-3"
                :disabled="busy !== ''"
                :aria-busy="busy === l.name"
                @click="refreshNow(l.name)"
              >
                {{ busy === l.name ? 'Refreshing…' : 'Refresh' }}
              </button>
              <button type="button" class="link" :disabled="busy !== ''" @click="edit(l)">
                Edit
              </button>
              <ConfirmButton
                v-if="busy === ''"
                class="ml-3"
                label="Delete"
                confirm-label="Delete list?"
                @confirm="config.removeBlockList(l.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p class="max-w-3xl text-sm text-neutral-500">
      Lists are fetched on a schedule and cached on this box, so a reboot without a working line
      blocks what it blocked yesterday. A name on a list blocks everything under it as well, which
      is stricter than Pi-hole: use Exceptions to let one back through.
    </p>

    <BlockListDialog v-model:open="open" :list="editing" />
  </div>
</template>
