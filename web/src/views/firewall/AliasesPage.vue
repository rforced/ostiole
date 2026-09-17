<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { COUNTRIES } from '@/lib/countries'
import { useConfigStore } from '@/stores/config'
import AliasDialog from '@/views/firewall/AliasDialog.vue'

const config = useConfigStore()
const editing = ref(null)
const open = ref(false)
const status = ref([])
/** The alias being fetched right now, or 'all'; only its own link says so. */
const refreshing = ref('')

/** What the box has actually fetched, keyed by alias. */
const fetched = computed(() => Object.fromEntries(status.value.map((s) => [s.alias, s])))
const anyFetched = computed(() => config.aliases.some((a) => a.url || a.type === 'geoip'))

// A box that cannot say what it fetched just shows nothing against each alias.
const feeds = useAsync(async () => {
  try {
    status.value = await api.aliases.feeds()
  } catch {
    status.value = []
  }
})
onMounted(feeds.run)

const refresh = useAsync(async (name) => {
  refreshing.value = name || 'all'
  try {
    if (name) await api.aliases.refresh(name)
    else await api.aliases.refreshAll()
    await feeds.run()
  } finally {
    refreshing.value = ''
  }
})

const COUNTRY_NAMES = Object.fromEntries(COUNTRIES.map((c) => [c.code, c.name]))

/** Country aliases read better as names than as two-letter codes. */
function countryNames(codes) {
  const shown = codes.slice(0, 4).map((c) => COUNTRY_NAMES[c] ?? c)
  if (codes.length <= 4) return shown.join(', ')
  return `${shown.join(', ')} … (${codes.length} countries)`
}

function add() {
  editing.value = null
  open.value = true
}
function edit(a) {
  editing.value = a
  open.value = true
}
</script>

<template>
  <div class="space-y-3">
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn-secondary" @click="add">
        <Plus class="mr-1 size-4" aria-hidden="true" /> Add alias
      </button>
      <RefreshButton
        v-if="anyFetched"
        :busy="refresh.busy.value"
        :updated-at="refresh.updatedAt.value"
        label="Refresh lists"
        @click="refresh.run('')"
      />
    </div>
    <p v-if="refresh.error.value" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ refresh.error.value }}
    </p>
    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Alias</th>
            <th>Type</th>
            <th>Entries</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="config.aliases.length === 0" key="empty" class="row-static">
            <td colspan="5" class="text-neutral-500">No aliases yet.</td>
          </tr>
          <tr
            v-for="a in config.aliases"
            :key="a.name"
            :class="{ 'row-changed': config.isChanged('aliases', a.name) }"
          >
            <td class="font-mono font-medium">{{ a.name }}</td>
            <td>{{ a.type }}</td>
            <td class="font-mono text-code">
              <template v-if="a.type === 'geoip'">{{ countryNames(a.entries) }}</template>
              <template v-else>
                {{ a.entries.slice(0, 4).join(', ')
                }}<span v-if="a.entries.length > 4"> … ({{ a.entries.length }})</span>
              </template>
              <div
                v-if="fetched[a.name]?.lastError"
                class="mt-1 font-sans text-sm text-red-600 dark:text-red-400"
              >
                {{ fetched[a.name].lastError }}
              </div>
              <div v-else-if="fetched[a.name]" class="mt-1 text-neutral-500">
                {{ fetched[a.name].entries }} fetched<span v-if="fetched[a.name].stale">
                  · <span class="badge badge-warn">stale</span></span
                >
              </div>
              <div v-else-if="a.url || a.type === 'geoip'" class="mt-1 text-neutral-500">
                not fetched yet
              </div>
            </td>
            <td>
              {{ a.description }}
              <div v-if="a.url" class="font-mono text-code break-all text-neutral-500">
                {{ a.url }}
              </div>
            </td>
            <td class="text-right whitespace-nowrap">
              <button
                v-if="a.url || a.type === 'geoip'"
                type="button"
                class="link mr-3"
                :disabled="refresh.busy.value"
                @click="refresh.run(a.name)"
              >
                {{ refreshing === a.name ? 'Refreshing…' : 'Refresh' }}
              </button>
              <button type="button" class="link" @click="edit(a)">Edit</button>
              <ConfirmButton
                v-if="config.aliasReferences(a.name).length === 0"
                class="ml-3"
                label="Delete"
                :question="`Delete alias ${a.name}?`"
                :description="a.description"
                @confirm="config.removeAlias(a.name)"
              />
              <span v-else class="ml-3 text-sm text-neutral-500"
                >in use by {{ config.aliasReferences(a.name).join(', ') }}</span
              >
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
    <AliasDialog v-model:open="open" :alias="editing" />
  </div>
</template>
