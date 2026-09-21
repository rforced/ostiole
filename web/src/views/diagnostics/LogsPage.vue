<script setup>
import { onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'

/** The units an Ostiole router runs, plus everything. */
const UNITS = [
  { value: '', label: 'Everything' },
  { value: 'ostiole.service', label: 'ostiole (daemon)' },
  { value: 'ostiole-firewall.service', label: 'ostiole-firewall (boot)' },
  { value: 'ostiole-dnsmasq.service', label: 'DHCP and DNS' },
  { value: 'ostiole-unbound.service', label: 'Validating resolver' },
  { value: 'ostiole-proxy.service', label: 'Reverse proxy' },
  { value: 'systemd-networkd.service', label: 'Network' },
]

const unit = ref('ostiole.service')
const lines = ref(200)
const priority = ref('')
const since = ref('-1h')
const entries = ref([])

const LEVELS = ['emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug']

// The journal arrives newest first, the order every log in the UI reads
// in, so the entries render as they come.
const load = useAsync(async () => {
  entries.value = await api.diagnostics.journal({
    unit: unit.value,
    lines: Number(lines.value) || 200,
    priority: priority.value,
    since: since.value,
  })
})

onMounted(load.run)
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Logs" intro="What the router's units wrote to the journal.">
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          busy-label="Reading…"
          @click="load.run()"
        />
      </template>
      <div class="space-y-4">
        <!-- No submit button, so Enter in a field is wired by hand. -->
        <form class="form-row" @submit.prevent="load.run()" @keydown.enter.prevent="load.run()">
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
        </form>

        <p v-if="load.error.value" role="alert" class="text-bad">{{ load.error.value }}</p>

        <div
          class="max-h-[32rem] overflow-auto rounded-lg border border-line bg-page p-3 font-mono text-code"
        >
          <p v-if="!entries.length" class="text-ink-muted">
            {{ load.busy.value ? 'Reading…' : 'Nothing in this window.' }}
          </p>
          <p
            v-for="(e, i) in entries"
            :key="i"
            :class="{
              'text-bad': e.priority <= 3,
              'text-warn': e.priority === 4,
            }"
          >
            <span class="text-ink-muted">{{ new Date(e.time).toLocaleTimeString() }}</span>
            <span class="text-ink-muted"> {{ e.unit }}</span>
            <span v-if="e.priority <= 4"> [{{ LEVELS[e.priority] }}]</span>
            {{ e.message }}
          </p>
        </div>
      </div>
    </SectionCard>
  </div>
</template>
