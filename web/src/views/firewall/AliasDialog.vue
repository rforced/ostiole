<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import CountryPicker from '@/views/firewall/CountryPicker.vue'

const props = defineProps({ alias: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
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
  }
}

/** A country alias always fetches; a host or port alias may. */
const fetches = computed(() => form.value.type === 'geoip' || form.value.url.trim() !== '')

const ENTRY_HINTS = {
  hosts:
    'One per line: addresses or CIDR networks. With a URL, these are kept alongside whatever is fetched.',
  ports: 'One per line: ports or ranges like 8000-8100.',
  geoip: 'The addresses behind each country are fetched from the GeoIP source set under System.',
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
        }
      : blank()
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
  const entries =
    form.value.type === 'geoip' ? [...form.value.countries] : parseList(form.value.entries)
  if (form.value.type === 'geoip' && entries.length === 0) {
    error.value = 'Choose at least one country.'
    return
  }
  const out = { name, type: form.value.type, entries }
  if (form.value.description) out.description = form.value.description
  if (form.value.type !== 'geoip' && form.value.url.trim()) out.url = form.value.url.trim()
  if (fetches.value && Number(form.value.refreshHours) > 0) {
    out.refreshHours = Number(form.value.refreshHours)
  }
  config.upsertAlias(out, previous)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="alias ? `Alias ${alias.name}` : 'New alias'"
    description="A named list you can reuse in rules. Host aliases become nftables sets."
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
          <select id="alias-type" v-model="form.type" class="input">
            <option value="hosts">Hosts and networks</option>
            <option value="ports">Ports</option>
            <option value="geoip">Countries (addresses fetched)</option>
          </select>
        </FormField>
      </div>
      <FormField id="alias-desc" label="Description">
        <input id="alias-desc" v-model="form.description" class="input" />
      </FormField>
      <FormField
        v-if="form.type !== 'geoip'"
        id="alias-url"
        label="Fetch from"
        hint="Optional: a published list, one entry per line. It is cached here and refreshed on a schedule."
      >
        <input
          id="alias-url"
          v-model="form.url"
          class="input font-mono"
          placeholder="https://www.spamhaus.org/drop/drop.txt"
          spellcheck="false"
        />
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
          class="input w-32 font-mono"
        />
      </FormField>
      <FormField
        v-if="form.type === 'geoip'"
        id="alias-countries"
        label="Countries"
        :hint="ENTRY_HINTS.geoip"
      >
        <CountryPicker v-model="form.countries" />
      </FormField>
      <FormField v-else id="alias-entries" label="Entries" :hint="ENTRY_HINTS[form.type]">
        <textarea
          id="alias-entries"
          v-model="form.entries"
          class="input h-32 font-mono"
          spellcheck="false"
        ></textarea>
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
