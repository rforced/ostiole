<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import LiveButton from '@/components/LiveButton.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { api } from '@/lib/api'
import { useDraftRows } from '@/lib/draft'
import { overrideKey, overrideName } from '@/lib/hosts'
import { useSearch } from '@/lib/search'
import { byAddress, byText, useSort } from '@/lib/sort'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import HostOverrideDialog from '@/views/services/dns/HostOverrideDialog.vue'

/** How often Live reads the names again. */
const LIVE_MS = 2000

const COLUMNS = [
  ['name', 'Name'],
  ['address', 'Address'],
  ['source', 'Source'],
]

/** Where each kind of name comes from, and the page that changes it. */
const SOURCES = {
  override: { label: 'Override' },
  static: { label: 'Static lease', to: '/services/dhcp#v4' },
  proxy: { label: 'Reverse proxy', to: '/services/proxy' },
  device: { label: 'Device', to: '/services/dhcp#leases' },
}

const auth = useAuthStore()
const config = useConfigStore()
const dns = computed(() => config.ensureServices().dns)
const hosts = computed(() => dns.value.hostOverrides ?? [])
const live = ref(false)

/**
 * The names the router answers that nobody wrote on this page: static
 * leases, proxy sites, and the names devices sent to a server that
 * registers them. Read for the draft, like the system rules, so a change
 * shows before it is applied; Live follows the devices as they come.
 */
const derived = useDraftRows((draft) => api.dnsNames(draft), { interval: LIVE_MS })
watch(live, (on) => {
  if (on) {
    derived.run()
    derived.start()
  } else {
    derived.stop()
  }
})

const names = computed(() => [
  ...hosts.value.map((h) => ({
    key: `override ${overrideKey(h)}`,
    source: 'override',
    name: overrideName(h, dns.value.domain),
    aliases: h.aliases ?? [],
    addresses: [h.ip],
    description: h.description,
    override: h,
  })),
  ...derived.rows.value.map((n) => ({
    ...n,
    key: `${n.source} ${n.name} ${n.mac ?? n.site ?? ''} ${n.addresses.join(' ')}`,
    aliases: [],
  })),
])

const { query, shown } = useSearch(names, (n) => ({
  values: [
    n.name,
    ...n.aliases,
    ...n.addresses,
    SOURCES[n.source].label,
    n.description,
    n.site,
    ...(n.interfaces ?? []),
  ],
  macs: [n.mac],
}))

// The list keeps the order it was written in until a header is clicked.
const sort = useSort(shown, {
  name: byText((n) => n.name),
  address: byAddress((n) => n.addresses[0]),
  source: byText((n) => SOURCES[n.source].label),
})
const rows = sort.sorted

const empty = computed(() => {
  if (!derived.updatedAt.value && !names.value.length) return 'Reading…'
  if (!names.value.length) return 'No names.'
  return `Nothing matches "${query.value.trim()}".`
})

/** What answers a device's name instead of it. */
function holder(h) {
  switch (h.source) {
    case 'override':
      return `override ${h.name}`
    case 'static':
      return `static lease ${h.name}`
    default:
      return `reverse proxy site ${h.name}`
  }
}

const when = (t) => new Date(t).toLocaleString()

const editing = ref(null)
const open = ref(false)
function addHost() {
  editing.value = null
  open.value = true
}
function editHost(h) {
  editing.value = h
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Names" :count="names.length" flush>
      <template #intro>
        <template v-if="dns.domain">
          A name with no domain lives under <span class="font-mono">{{ dns.domain }}</span> and
          answers bare as well. One with its own domain answers in full only. The first name on a
          row answers the reverse lookup.
        </template>
        <template v-else>The first name on a row answers the reverse lookup.</template>
      </template>
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <LiveButton v-model="live" :failing="Boolean(derived.error.value)" />
        <RefreshButton
          :busy="derived.busy.value"
          :updated-at="derived.updatedAt.value"
          @click="derived.run"
        />
        <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="addHost">
          <Plus class="size-4" aria-hidden="true" /> Add host
        </button>
      </template>
      <div class="card-strip">
        <SearchBox
          v-model="query"
          placeholder="name, address, MAC, or source"
          :shown="shown.length"
          :total="names.length"
        />
      </div>
      <div v-if="derived.error.value" class="card-strip">
        <p role="alert" class="text-bad">{{ derived.error.value }}</p>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <SortHeader by="name" :sort="sort">Name</SortHeader>
            <SortHeader by="address" :sort="sort">Address</SortHeader>
            <SortHeader by="source" :sort="sort">Source</SortHeader>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="4" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr
            v-for="n in rows"
            :key="n.key"
            :class="{
              'row-changed':
                n.override &&
                config.isChanged('services.dns.hostOverrides', overrideKey(n.override)),
            }"
          >
            <td data-label="Name">
              <div class="font-mono text-code">{{ n.name }}</div>
              <div v-if="n.aliases.length" class="font-mono text-code text-ink-muted">
                {{ n.aliases.join(', ') }}
              </div>
            </td>
            <td data-label="Address">
              <div v-for="a in n.addresses" :key="a" class="font-mono text-code">{{ a }}</div>
              <div v-if="!n.addresses.length" class="text-ink-muted">
                This router on {{ (n.interfaces ?? []).join(', ') }}
              </div>
            </td>
            <td data-label="Source">
              <div>
                <RouterLink v-if="SOURCES[n.source].to" :to="SOURCES[n.source].to" class="link">
                  {{ SOURCES[n.source].label }}
                </RouterLink>
                <template v-else>{{ SOURCES[n.source].label }}</template>
                <span v-if="n.site" class="ml-1 font-mono text-code text-ink-muted">{{
                  n.site
                }}</span>
              </div>
              <div v-if="n.mac" class="whitespace-nowrap">
                <span class="font-mono text-code">{{ n.mac }}</span>
                <RandomMacBadge :mac="n.mac" />
              </div>
              <div v-if="n.description" class="text-ink-muted">{{ n.description }}</div>
              <div v-if="n.expires" class="text-xs whitespace-nowrap text-ink-muted">
                Until {{ when(n.expires) }}
              </div>
              <div v-if="n.heldBy" class="text-sm text-warn">
                Loses the name to {{ holder(n.heldBy) }}.
              </div>
            </td>
            <td class="text-right whitespace-nowrap" data-label="">
              <template v-if="n.override">
                <button type="button" class="link" @click="editHost(n.override)">
                  {{ auth.readOnly ? 'View' : 'Edit' }}
                </button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete the host override for ${n.name}?`"
                  :description="n.description"
                  @confirm="config.removeHostOverride(overrideKey(n.override))"
                />
              </template>
              <span v-else class="badge">locked</span>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <HostOverrideDialog v-model:open="open" :override="editing" />
  </div>
</template>
