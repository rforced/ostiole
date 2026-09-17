<script setup>
import { Plus, RefreshCw } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { api } from '@/lib/api'
import { COUNTRIES } from '@/lib/countries'
import { useConfigStore } from '@/stores/config'
import AliasDialog from '@/views/firewall/AliasDialog.vue'

const config = useConfigStore()
const editing = ref(null)
const open = ref(false)
const status = ref([])
const busy = ref('')
const error = ref('')

/** What the box has actually fetched, keyed by alias. */
const fetched = computed(() => Object.fromEntries(status.value.map((s) => [s.alias, s])))
const anyFetched = computed(() => config.aliases.some((a) => a.url || a.type === 'geoip'))

onMounted(refreshStatus)

async function refreshStatus() {
  try {
    status.value = await api.aliases.feeds()
  } catch {
    status.value = []
  }
}

async function refreshNow(name) {
  error.value = ''
  busy.value = name || 'all'
  try {
    if (name) await api.aliases.refresh(name)
    else await api.aliases.refreshAll()
    await refreshStatus()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = ''
  }
}

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
      <button
        v-if="anyFetched"
        type="button"
        class="btn-secondary"
        :disabled="busy !== ''"
        @click="refreshNow('')"
      >
        <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Refresh lists
      </button>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
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
        <tbody>
          <tr v-if="config.aliases.length === 0">
            <td colspan="5" class="text-neutral-500">No aliases yet.</td>
          </tr>
          <tr v-for="a in config.aliases" :key="a.name">
            <td class="font-mono font-medium">{{ a.name }}</td>
            <td>{{ a.type }}</td>
            <td class="font-mono text-xs">
              <template v-if="a.type === 'geoip'">{{ countryNames(a.entries) }}</template>
              <template v-else>
                {{ a.entries.slice(0, 4).join(', ')
                }}<span v-if="a.entries.length > 4"> … ({{ a.entries.length }})</span>
              </template>
              <div v-if="fetched[a.name]" class="mt-1 text-neutral-500">
                <span v-if="fetched[a.name].lastError" class="badge badge-warn">
                  {{ fetched[a.name].lastError }}
                </span>
                <template v-else>
                  {{ fetched[a.name].entries }} fetched<span v-if="fetched[a.name].stale">
                    · <span class="badge badge-warn">stale</span></span
                  >
                </template>
              </div>
              <div v-else-if="a.url || a.type === 'geoip'" class="mt-1 text-neutral-500">
                not fetched yet
              </div>
            </td>
            <td>
              {{ a.description }}
              <div v-if="a.url" class="font-mono text-xs break-all text-neutral-500">
                {{ a.url }}
              </div>
            </td>
            <td class="text-right whitespace-nowrap">
              <button
                v-if="a.url || a.type === 'geoip'"
                type="button"
                class="link mr-3"
                :disabled="busy !== ''"
                @click="refreshNow(a.name)"
              >
                Refresh
              </button>
              <button type="button" class="link" @click="edit(a)">Edit</button>
              <ConfirmButton
                v-if="config.aliasReferences(a.name).length === 0"
                class="ml-3"
                label="Delete"
                confirm-label="Delete alias?"
                @confirm="config.removeAlias(a.name)"
              />
              <span
                v-else
                class="ml-3 text-xs text-neutral-500"
                :title="config.aliasReferences(a.name).join(', ')"
                >in use</span
              >
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <AliasDialog v-model:open="open" :alias="editing" />
  </div>
</template>
