<script setup>
import { LoaderCircle } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { fetches, takesURLs, urlsOf } from '@/lib/aliases'
import { api } from '@/lib/api'
import { parseAsnList } from '@/lib/asn'
import { formatCount } from '@/lib/format'
import { joinList, parseList } from '@/lib/lists'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import CountryPicker from '@/views/firewall/CountryPicker.vue'

const props = defineProps({
  alias: { type: Object, default: null },
  /**
   * The feed statuses the page has already read, so the country picker
   * can say how much each country held without fetching anything itself.
   *
   * @type {import('vue').PropType<object[]>}
   */
  feeds: { type: Array, default: () => [] },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const auth = useAuthStore()
const form = ref(blank())
const error = ref('')

function blank() {
  return {
    name: '',
    type: 'hosts',
    description: '',
    entries: '',
    countries: [],
    refreshHours: '',
  }
}

/**
 * What each country held at the last fetch of this alias, keyed by code.
 * A country picked but never fetched simply has no number beside it.
 */
const countryCounts = computed(() => {
  const status = props.feeds.find((f) => f.alias === props.alias?.name)
  const out = {}
  for (const part of status?.parts ?? []) {
    if (!part.country) continue
    // A country has one source per address family, and the ranges of both
    // are what it costs.
    out[part.country] = (out[part.country] ?? 0) + part.entries
  }
  return out
})

/**
 * What each AS held at the last fetch of this alias, with its holder
 * where the names lookup answered: what "AS15169" is, and how big.
 */
const asnParts = computed(() => {
  const status = props.feeds.find((f) => f.alias === props.alias?.name)
  return (status?.parts ?? []).filter((p) => p.asn)
})

/** A country or AS alias holds keys, not addresses, and always fetches. */
const keyed = computed(() => form.value.type === 'geoip' || form.value.type === 'asn')
const lines = computed(() =>
  form.value.type === 'geoip' ? form.value.countries : parseList(form.value.entries),
)
const fetching = computed(() => fetches({ type: form.value.type, entries: lines.value }))
/** The lists a host alias fetches, each once. */
const urls = computed(() => (takesURLs(form.value.type) ? [...new Set(urlsOf(lines.value))] : []))

/** What the last fetch of this alias read from a URL, or null. */
function fetchedPart(url) {
  const status = props.feeds.find((f) => f.alias === props.alias?.name)
  return status?.parts?.find((p) => p.source === url) ?? null
}

/** What the router found at each URL typed here, which nothing has fetched. */
const inspected = ref({})

/**
 * Asks the router to read a URL typed here, so what it holds is known
 * before anything is saved. Each URL is read once, and none that the last
 * fetch of this alias read.
 */
async function inspect(url) {
  if (auth.readOnly || !/^https?:\/\/\S+$/i.test(url)) return
  const seen = inspected.value[url]
  if (fetchedPart(url) || (seen && !seen.error)) return
  inspected.value = { ...inspected.value, [url]: { busy: true, entries: null, error: '' } }
  try {
    const part = await api.aliases.inspect(url)
    const entries = part.entries ?? 0
    inspected.value = { ...inspected.value, [url]: { busy: false, entries, error: '' } }
  } catch (e) {
    inspected.value = {
      ...inspected.value,
      [url]: { busy: false, entries: null, error: e.message },
    }
  }
}

function inspectAll() {
  for (const url of urls.value) inspect(url)
}

/** One line per URL that has been read or is being read. */
const previews = computed(() =>
  urls.value
    .map((url) => {
      const part = fetchedPart(url)
      if (part) return { url, busy: false, entries: part.entries ?? 0, error: '' }
      return { url, ...inspected.value[url] }
    })
    .filter((p) => p.busy || p.error || p.entries != null),
)

const ENTRY_HINTS = {
  hosts:
    'One address, CIDR network or URL per line. A URL is a published list, plain text or JSON, fetched on a schedule and cached here. Plain http only from inside the network.',
  ports: 'One port, range or URL per line, like 8000-8100.',
  geoip: 'The addresses behind each country, fetched from the GeoIP source.',
  asn: 'One AS number per line, like AS15169. The prefixes each one announces are fetched from the ASN source.',
}

function prefixCount(n) {
  return `${formatCount(n)} ${n === 1 ? 'prefix' : 'prefixes'}`
}

function addressCount(n) {
  return `${formatCount(n)} ${n === 1 ? 'address' : 'addresses'}`
}

watch(
  () => [open.value, props.alias],
  () => {
    if (!open.value) return
    error.value = ''
    const a = props.alias
    form.value = a
      ? {
          ...blank(),
          name: a.name,
          type: a.type,
          description: a.description ?? '',
          entries: a.type === 'geoip' ? '' : joinList(a.entries),
          countries: a.type === 'geoip' ? [...(a.entries ?? [])] : [],
          refreshHours: a.refreshHours ?? '',
        }
      : blank()
    inspectAll()
  },
  { immediate: true },
)

function save() {
  error.value = ''
  const name = form.value.name.trim()
  if (!/^[a-z][a-z0-9_]{0,30}$/.test(name)) {
    error.value = 'Name must be lowercase letters, digits, or underscores and start with a letter.'
    return
  }
  const previous = props.alias?.name ?? name
  if (name !== previous && config.aliases.some((a) => a.name === name)) {
    error.value = `Alias ${name} already exists.`
    return
  }
  let entries
  if (form.value.type === 'geoip') {
    entries = [...form.value.countries]
  } else if (form.value.type === 'asn') {
    const { asns, invalid } = parseAsnList(form.value.entries)
    if (invalid.length > 0) {
      error.value = `${invalid[0]} is not an AS number.`
      return
    }
    entries = asns
  } else {
    entries = parseList(form.value.entries)
  }
  if (keyed.value && entries.length === 0) {
    error.value =
      form.value.type === 'geoip' ? 'Choose at least one country.' : 'List at least one AS number.'
    return
  }
  if (!entries.length && props.alias && props.alias.entries == null) entries = props.alias.entries
  const out = { name, type: form.value.type, entries }
  if (form.value.description) out.description = form.value.description
  if (fetching.value && Number(form.value.refreshHours) > 0) {
    out.refreshHours = Number(form.value.refreshHours)
  }
  config.upsertAlias(out, previous)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="alias ? `Alias ${alias.name}` : 'Add alias'"
    description="Renaming it updates every rule that names it."
  >
    <form id="alias-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="alias-name" label="Name">
          <input
            id="alias-name"
            v-model="form.name"
            class="input font-mono"
            autocapitalize="none"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField id="alias-type" label="Type">
          <select id="alias-type" v-model="form.type" class="input" @change="inspectAll">
            <option value="hosts">Hosts and networks</option>
            <option value="ports">Ports</option>
            <option value="geoip">Countries (addresses fetched)</option>
            <option value="asn">Networks by AS number (prefixes fetched)</option>
          </select>
        </FormField>
      </div>
      <FormField id="alias-desc" label="Description">
        <input id="alias-desc" v-model="form.description" class="input" />
      </FormField>
      <FormField
        v-if="fetching"
        id="alias-refresh"
        label="Refresh every (hours)"
        hint="24 is the default, 1 at least."
      >
        <input
          id="alias-refresh"
          v-model.number="form.refreshHours"
          type="number"
          min="1"
          max="720"
          placeholder="24"
          class="input w-32 font-mono max-sm:w-full"
        />
      </FormField>
      <FormField
        v-if="form.type === 'geoip'"
        id="alias-countries"
        label="Countries"
        :hint="ENTRY_HINTS.geoip"
      >
        <CountryPicker v-model="form.countries" :counts="countryCounts" />
      </FormField>
      <FormField v-else id="alias-entries" label="Entries" :hint="ENTRY_HINTS[form.type]">
        <textarea
          id="alias-entries"
          v-model="form.entries"
          class="input h-32 font-mono"
          spellcheck="false"
          @blur="inspectAll"
        ></textarea>
        <ul v-if="previews.length > 0" class="mt-2 space-y-0.5 text-sm text-ink-muted">
          <li
            v-for="p in previews"
            :key="p.url"
            :class="{ 'text-bad': p.error }"
            :aria-busy="p.busy || undefined"
          >
            <span class="font-mono break-all">{{ p.url }}</span
            ><template v-if="p.busy">
              ·
              <LoaderCircle
                class="inline size-4 animate-spin align-text-bottom"
                aria-hidden="true"
              />Reading the list…</template
            ><template v-else-if="p.error"> · Could not read the list: {{ p.error }}</template
            ><template v-else> · {{ addressCount(p.entries) }}</template>
          </li>
        </ul>
        <ul
          v-if="form.type === 'asn' && asnParts.length > 0"
          class="mt-2 space-y-0.5 text-sm text-ink-muted"
        >
          <li v-for="p in asnParts" :key="p.asn">
            <span class="font-mono">{{ p.asn }}</span
            ><template v-if="p.holder"> · {{ p.holder }}</template> · {{ prefixCount(p.entries) }}
          </li>
        </ul>
      </FormField>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="alias-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
