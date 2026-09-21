<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { canonicalAsn } from '@/lib/asn'
import { useAsync } from '@/lib/async'
import { COUNTRIES } from '@/lib/countries'
import { useConfigStore } from '@/stores/config'
import AliasDialog from '@/views/firewall/AliasDialog.vue'

const config = useConfigStore()
const router = useRouter()
const editing = ref(null)
const open = ref(false)
const status = ref([])
/** The alias being fetched right now, or 'all'; only its own link says so. */
const refreshing = ref('')

/** What the router has actually fetched, keyed by alias. */
const fetched = computed(() => Object.fromEntries(status.value.map((s) => [s.alias, s])))

/** Whether the router fetches this alias: a URL, or a country or AS list. */
function fetches(a) {
  return Boolean(a.url) || a.type === 'geoip' || a.type === 'asn'
}
const anyFetched = computed(() => config.aliases.some(fetches))

// A router that cannot say what it fetched just shows nothing against each alias.
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

/** AS numbers read better with their holders, which the last fetch supplied. */
function asnNames(a) {
  const holders = {}
  for (const p of fetched.value[a.name]?.parts ?? []) {
    if (p.asn && p.holder) holders[p.asn] = p.holder
  }
  const shown = a.entries.slice(0, 4).map((e) => {
    const asn = canonicalAsn(e)
    return holders[asn] ? `${asn} ${holders[asn]}` : asn
  })
  if (a.entries.length <= 4) return shown.join(', ')
  return `${shown.join(', ')} … (${a.entries.length} networks)`
}

/**
 * Where a rule made from this list would go: the first zone that faces
 * the internet, which is where a blocklist belongs, and otherwise the
 * first zone there is.
 */
const ruleZone = computed(
  () => (config.zones.find((z) => z.external) ?? config.zones[0])?.name ?? '',
)

/**
 * Hand the list to the rules page, which opens a new rule with it filled
 * in: an address list becomes the source, a port list becomes the ports.
 * The rule is a draft like any other until it is applied.
 */
function blockWith(a) {
  router.push({ path: '/firewall/rules', query: { block: a.name }, hash: `#${ruleZone.value}` })
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
  <div class="space-y-5">
    <SectionCard
      title="Aliases"
      :count="config.aliases.length"
      intro="A named list of addresses, networks or ports, to use in a rule."
      flush
    >
      <template #actions>
        <RefreshButton
          v-if="anyFetched"
          :busy="refresh.busy.value"
          :updated-at="refresh.updatedAt.value"
          label="Refresh lists"
          @click="refresh.run('')"
        />
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add alias
        </button>
      </template>
      <div v-if="refresh.error.value" class="px-4 pb-3">
        <p role="alert" class="text-bad">{{ refresh.error.value }}</p>
      </div>
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
            <td colspan="5" class="text-ink-muted">No aliases.</td>
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
              <template v-else-if="a.type === 'asn'">{{ asnNames(a) }}</template>
              <template v-else>
                {{ a.entries.slice(0, 4).join(', ')
                }}<span v-if="a.entries.length > 4"> … ({{ a.entries.length }})</span>
              </template>
              <div v-if="fetched[a.name]?.lastError" class="mt-1 font-sans text-sm text-bad">
                {{ fetched[a.name].lastError }}
              </div>
              <div v-else-if="fetched[a.name]" class="mt-1 text-ink-muted">
                {{ fetched[a.name].entries }} fetched<span v-if="fetched[a.name].stale">
                  · <span class="badge badge-warn">stale</span></span
                >
              </div>
              <div v-else-if="fetches(a)" class="mt-1 text-ink-muted">not fetched yet</div>
            </td>
            <td>
              {{ a.description }}
              <div v-if="a.url" class="font-mono text-code break-all text-ink-muted">
                {{ a.url }}
              </div>
            </td>
            <td class="text-right whitespace-nowrap">
              <button
                v-if="fetches(a)"
                type="button"
                class="link mr-3"
                :disabled="refresh.busy.value"
                @click="refresh.run(a.name)"
              >
                {{ refreshing === a.name ? 'Refreshing…' : 'Refresh' }}
              </button>
              <button v-if="ruleZone" type="button" class="link mr-3" @click="blockWith(a)">
                Make a rule
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
              <span
                v-else
                class="ml-3 text-sm text-ink-muted"
                :title="`In use by ${config.aliasReferences(a.name).join(', ')}`"
                >In use</span
              >
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <AliasDialog v-model:open="open" :alias="editing" :feeds="status" />
  </div>
</template>
