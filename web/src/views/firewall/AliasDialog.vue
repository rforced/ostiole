<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { parseAsnList } from '@/lib/asn'
import { formatCount } from '@/lib/format'
import { joinList, parseList } from '@/lib/lists'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ChoicePicker from '@/views/firewall/ChoicePicker.vue'
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
    url: '',
    refreshHours: 24,
    select: [],
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
/** A host or port alias fetches when it has a URL. */
const fetches = computed(() => keyed.value || form.value.url.trim() !== '')
/** A host alias with a URL can keep part of a JSON list. */
const selectable = computed(() => form.value.type === 'hosts' && form.value.url.trim() !== '')

/**
 * What the last fetch of this alias found to narrow it by, while the URL
 * is still the one fetched; null when there is no such fetch.
 */
const fetchedChoices = computed(() => {
  const status = props.feeds.find((f) => f.alias === props.alias?.name)
  const part = status?.parts?.find((p) => p.source === form.value.url.trim())
  return part ? (part.choices ?? []) : null
})

/** What the router found at a URL typed here, which nothing has fetched. */
const inspected = ref({ url: '', choices: [], error: '', busy: false })
const inspecting = computed(
  () => inspected.value.busy && inspected.value.url === form.value.url.trim(),
)
const inspectError = computed(() =>
  selectable.value && inspected.value.url === form.value.url.trim() ? inspected.value.error : '',
)

const choices = computed(() => {
  if (!selectable.value) return []
  if (fetchedChoices.value) return fetchedChoices.value
  return inspected.value.url === form.value.url.trim() ? inspected.value.choices : []
})

/**
 * The filter shows when the list has something to select, or the alias
 * already keeps part of one.
 */
const showFilter = computed(
  () => selectable.value && (choices.value.length > 0 || form.value.select.length > 0),
)

/**
 * Asks the router to read a URL typed here, so what the list can be
 * narrowed by is known before anything is saved. It reads only the URL
 * the operator typed, once.
 */
async function inspect() {
  const url = form.value.url.trim()
  if (!selectable.value || auth.readOnly || !/^https?:\/\/\S+$/i.test(url)) return
  if (fetchedChoices.value || (inspected.value.url === url && !inspected.value.error)) return
  inspected.value = { url, choices: [], error: '', busy: true }
  try {
    const part = await api.aliases.inspect(url)
    if (inspected.value.url === url) {
      inspected.value = { url, choices: part.choices ?? [], error: '', busy: false }
    }
  } catch (e) {
    if (inspected.value.url === url) {
      inspected.value = { url, choices: [], error: e.message, busy: false }
    }
  }
}

const ENTRY_HINTS = {
  hosts:
    'One address or CIDR network per line. With a URL, these are kept alongside what is fetched.',
  ports: 'One port or range per line, e.g. 8000-8100.',
  geoip: 'The addresses behind each country are fetched from the GeoIP source set under System.',
  asn: 'One AS number per line, like AS15169. The prefixes each one announces are fetched from the ASN source set under System.',
}

function prefixCount(n) {
  return `${formatCount(n)} ${n === 1 ? 'prefix' : 'prefixes'}`
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
          url: a.url ?? '',
          refreshHours: a.refreshHours || 24,
          select: [...(a.select ?? [])],
        }
      : blank()
    inspect()
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
  const out = { name, type: form.value.type, entries }
  if (form.value.description) out.description = form.value.description
  if (!keyed.value && form.value.url.trim()) out.url = form.value.url.trim()
  if (fetches.value && Number(form.value.refreshHours) > 0) {
    out.refreshHours = Number(form.value.refreshHours)
  }
  if (selectable.value && form.value.select.length > 0) out.select = [...form.value.select]
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
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
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
          <select id="alias-type" v-model="form.type" class="input" @change="inspect">
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
        v-if="!keyed"
        id="alias-url"
        label="Fetch from"
        hint="A published list, plain text or JSON, cached here and refetched on a schedule."
      >
        <input
          id="alias-url"
          v-model="form.url"
          class="input font-mono"
          placeholder="https://www.spamhaus.org/drop/drop.txt"
          spellcheck="false"
          @change="inspect"
        />
        <p
          v-if="inspecting"
          class="flex items-center gap-2 text-sm text-ink-muted"
          aria-busy="true"
        >
          <LoaderCircle class="size-4 animate-spin" aria-hidden="true" />Reading the list…
        </p>
        <p v-else-if="inspectError" class="text-sm text-bad">
          Could not read the list: {{ inspectError }}
        </p>
      </FormField>
      <FormField
        v-if="showFilter"
        id="alias-select"
        label="Keep only"
        hint="Nothing ticked keeps every address. Ticks in two groups keep only what is in both."
      >
        <ChoicePicker v-model="form.select" :choices="choices" />
      </FormField>
      <FormField
        v-if="fetches"
        id="alias-refresh"
        label="Refresh every (hours)"
        hint="Publishers ask not to be fetched more than once an hour."
      >
        <input
          id="alias-refresh"
          v-model.number="form.refreshHours"
          type="number"
          min="1"
          max="720"
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
        ></textarea>
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
      <p v-if="error" role="alert" class="text-sm text-bad">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
