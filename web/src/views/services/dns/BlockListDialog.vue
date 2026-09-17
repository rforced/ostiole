<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { formatCount } from '@/lib/format'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ list: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())
const error = ref('')
const catalog = ref([])
const chosen = ref('')

function blank() {
  return {
    name: '',
    description: '',
    url: '',
    format: '',
    refreshHours: 24,
    enabled: true,
  }
}

/** The published lists on offer, grouped so the menu reads sensibly. */
const grouped = computed(() => {
  const out = new Map()
  for (const e of catalog.value) {
    if (!out.has(e.category)) out.set(e.category, [])
    out.get(e.category).push(e)
  }
  return [...out.entries()]
})

watch(
  () => [open.value, props.list],
  async () => {
    if (!open.value) return
    error.value = ''
    chosen.value = ''
    const l = props.list
    form.value = l ? { ...blank(), ...l, refreshHours: l.refreshHours || 24 } : blank()
    if (!catalog.value.length) {
      try {
        catalog.value = await api.blocking.catalog()
      } catch {
        catalog.value = []
      }
    }
  },
  { immediate: true },
)

/** Picking a published list fills the form in; everything stays editable. */
function pick(name) {
  const e = catalog.value.find((c) => c.name === name)
  if (!e) return
  form.value = {
    ...form.value,
    name: e.name,
    description: e.title,
    url: e.url,
    format: e.format === 'auto' ? '' : e.format,
  }
}

function save() {
  error.value = ''
  const name = form.value.name.trim()
  if (!/^[a-z][a-z0-9_]{0,30}$/.test(name)) {
    error.value = 'Name must be lowercase letters, digits, or underscores and start with a letter.'
    return
  }
  const previous = props.list?.name ?? name
  if (name !== previous && config.blockLists.some((l) => l.name === name)) {
    error.value = `A list called ${name} already exists.`
    return
  }
  const url = form.value.url.trim()
  if (url && !/^https?:\/\//i.test(url)) {
    error.value = 'The URL has to start with http:// or https://.'
    return
  }
  const out = { name, enabled: form.value.enabled }
  if (form.value.description) out.description = form.value.description
  if (url) out.url = url
  if (form.value.format) out.format = form.value.format
  if (url && Number(form.value.refreshHours) > 0) out.refreshHours = Number(form.value.refreshHours)
  config.upsertBlockList(out, previous)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="list ? `List ${list.name}` : 'New block list'"
    description="Fetched on a schedule and cached on this box."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField
        v-if="!list"
        id="bl-catalog"
        label="Start from a published list"
        hint="Picking one fills the form in. Everything stays editable."
      >
        <select id="bl-catalog" v-model="chosen" class="input" @change="pick($event.target.value)">
          <option value="">Choose a list…</option>
          <optgroup v-for="[category, entries] in grouped" :key="category" :label="category">
            <option v-for="e in entries" :key="e.name" :value="e.name">
              {{ e.title }}, about {{ formatCount(e.names) }} names
            </option>
          </optgroup>
        </select>
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="bl-name" label="Name">
          <input
            id="bl-name"
            v-model="form.name"
            class="input font-mono"
            autocapitalize="none"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField
          id="bl-format"
          label="Written as"
          hint="Leave on automatic unless it is read wrongly."
        >
          <select id="bl-format" v-model="form.format" class="input">
            <option value="">Work it out</option>
            <option value="hosts">Hosts file (0.0.0.0 example.com)</option>
            <option value="domains">One name per line</option>
            <option value="adblock">Adblock rules (||example.com^)</option>
            <option value="dnsmasq">dnsmasq (local=/example.com/)</option>
            <option value="unbound">unbound local zones</option>
          </select>
        </FormField>
      </div>

      <FormField id="bl-desc" label="Description">
        <input id="bl-desc" v-model="form.description" class="input" />
      </FormField>

      <FormField id="bl-url" label="Fetch from" hint="Empty: a list loaded by hand.">
        <input
          id="bl-url"
          v-model="form.url"
          class="input font-mono"
          spellcheck="false"
          placeholder="https://example.org/hosts.txt"
        />
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          v-if="form.url"
          id="bl-refresh"
          label="Refresh every (hours)"
          hint="Default 24, never less than 1."
        >
          <input
            id="bl-refresh"
            v-model.number="form.refreshHours"
            type="number"
            min="1"
            max="720"
            class="input w-32 font-mono"
          />
        </FormField>
        <FormField id="bl-enabled" label="Use this list">
          <label class="flex items-center gap-2 text-sm">
            <input
              id="bl-enabled"
              v-model="form.enabled"
              type="checkbox"
              class="size-4 rounded border-neutral-300"
            />
            Merge it into what this box blocks
          </label>
        </FormField>
      </div>

      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
