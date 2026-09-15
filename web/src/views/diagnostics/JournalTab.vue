<script setup>
import { RefreshCw } from 'lucide-vue-next'
import { onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'

/** The units an Ostiole box runs, plus everything. */
const UNITS = [
  { value: '', label: 'Everything' },
  { value: 'ostiole.service', label: 'ostiole (daemon)' },
  { value: 'ostiole-firewall.service', label: 'ostiole-firewall (boot)' },
  { value: 'ostiole-dnsmasq.service', label: 'DHCP and DNS' },
  { value: 'ostiole-unbound.service', label: 'Validating resolver' },
  { value: 'systemd-networkd.service', label: 'Network' },
]

const unit = ref('ostiole.service')
const lines = ref(200)
const priority = ref('')
const since = ref('-1h')
const entries = ref([])
const error = ref('')
const busy = ref(false)

const LEVELS = ['emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug']

async function refresh() {
  busy.value = true
  try {
    entries.value = await api.diagnostics.journal({
      unit: unit.value,
      lines: Number(lines.value) || 200,
      priority: priority.value,
      since: since.value,
    })
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    entries.value = []
  } finally {
    busy.value = false
  }
}

onMounted(refresh)
</script>

<template>
  <div class="space-y-4">
    <form class="flex flex-wrap items-end gap-4" @submit.prevent="refresh">
      <FormField id="jr-unit" label="Unit">
        <select id="jr-unit" v-model="unit" class="input w-56">
          <option v-for="u in UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
        </select>
      </FormField>
      <FormField id="jr-since" label="Since" hint="e.g. -1h, -30min, 2026-09-15">
        <input id="jr-since" v-model="since" class="input w-40 font-mono" spellcheck="false" />
      </FormField>
      <FormField id="jr-prio" label="Level">
        <select id="jr-prio" v-model="priority" class="input w-40">
          <option value="">Everything</option>
          <option value="4">Warnings and worse</option>
          <option value="3">Errors and worse</option>
        </select>
      </FormField>
      <FormField id="jr-lines" label="Lines">
        <input
          id="jr-lines"
          v-model="lines"
          type="number"
          min="1"
          max="2000"
          class="input w-28 font-mono"
        />
      </FormField>
      <button type="submit" class="btn-secondary" :disabled="busy">
        <RefreshCw class="mr-1 size-4" aria-hidden="true" /> {{ busy ? 'Reading…' : 'Refresh' }}
      </button>
    </form>

    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <div
      class="max-h-[32rem] overflow-auto rounded-lg border border-neutral-200 bg-neutral-50 p-3 font-mono text-xs dark:border-neutral-800 dark:bg-neutral-950"
    >
      <p v-if="!entries.length" class="text-neutral-500">Nothing in this window.</p>
      <p
        v-for="(e, i) in entries"
        :key="i"
        :class="{
          'text-red-600 dark:text-red-400': e.priority <= 3,
          'text-amber-700 dark:text-amber-400': e.priority === 4,
        }"
      >
        <span class="text-neutral-500">{{ new Date(e.time).toLocaleTimeString() }}</span>
        <span class="text-neutral-500"> {{ e.unit }}</span>
        <span v-if="e.priority <= 4"> [{{ LEVELS[e.priority] }}]</span>
        {{ e.message }}
      </p>
    </div>
  </div>
</template>
