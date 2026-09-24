<script setup>
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const events = ref([])
const since = ref('-1h')
const limit = ref(200)
const updatedAt = ref(0)

const VERDICTS = {
  blocked: { label: 'blocked', tone: 'badge-bad' },
  'would-block': { label: 'would block', tone: 'badge-warn' },
  matched: { label: 'matched', tone: '' },
}

const load = useAsync(async () => {
  events.value = await api.proxy.events({ since: since.value, limit: Number(limit.value) || 200 })
  updatedAt.value = Date.now()
})

onMounted(load.run)

/** The WAF profile a site is inspected by, which an exclusion goes into. */
function profileOf(siteID) {
  return (config.proxy.sites ?? []).find((s) => s.id === siteID)?.waf ?? ''
}

const empty = computed(() => {
  if (load.busy.value) return 'Reading…'
  return 'Nothing matched in this window.'
})

function exclude(event, rule, onPath) {
  const profile = profileOf(event.site)
  if (!profile) return
  const exclusion = { rule: String(rule.id) }
  if (onPath) exclusion.path = pathOf(event.uri)
  if (rule.message) exclusion.description = rule.message
  config.addExclusion(profile, exclusion)
}

/** The path a request asked for, without its query. */
function pathOf(uri) {
  return String(uri ?? '/').split('?')[0] || '/'
}

function hasQuery(uri) {
  return String(uri ?? '').includes('?')
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Events"
      :count="events.length"
      intro="What the WAF matched, read back from the journal."
      flush
    >
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="updatedAt"
          busy-label="Reading…"
          @click="load.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <form class="form-row" @submit.prevent="load.run()">
          <FormField id="ev-since" label="Since" hint="e.g. -1h, -30min, 2026-09-15">
            <input
              id="ev-since"
              v-model="since"
              class="input w-40 font-mono max-sm:w-full"
              spellcheck="false"
            />
          </FormField>
          <FormField id="ev-limit" label="Events">
            <input
              id="ev-limit"
              v-model="limit"
              type="number"
              min="1"
              max="1000"
              class="input w-28 font-mono max-sm:w-full"
            />
          </FormField>
        </form>
        <p v-if="load.error.value" role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
      <!-- On a phone an event is a block of lines: when, the verdict and the
           site; who asked for what; the rules it matched. -->
      <table class="table table-flow">
        <thead>
          <tr>
            <th>Time</th>
            <th>Site</th>
            <th>Client</th>
            <th>Request</th>
            <th>Verdict</th>
            <th>Rules</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!events.length">
            <td colspan="6" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr
            v-for="e in events"
            :key="e.id"
            class="max-sm:after:order-4 max-sm:after:basis-full max-sm:after:content-['']"
          >
            <td class="whitespace-nowrap text-ink-muted tabular-nums max-sm:order-1">
              {{ new Date(e.time).toLocaleTimeString() }}
            </td>
            <td class="font-mono text-code max-sm:order-3">{{ e.site || '—' }}</td>
            <td class="font-mono text-code max-sm:order-5">{{ e.client }}</td>
            <td class="font-mono text-code max-sm:order-6 max-sm:min-w-0 max-sm:flex-1">
              <details class="max-w-md">
                <summary class="block cursor-pointer truncate" :title="`${e.method} ${e.uri}`">
                  {{ e.method }} {{ pathOf(e.uri)
                  }}<span v-if="hasQuery(e.uri)" class="text-ink-muted">?…</span>
                  <span v-if="e.status" class="text-ink-muted">· {{ e.status }}</span>
                </summary>
                <div class="mt-1 break-all text-ink-2">{{ e.uri }}</div>
              </details>
            </td>
            <td class="max-sm:order-2">
              <span class="badge" :class="VERDICTS[e.verdict]?.tone">
                {{ VERDICTS[e.verdict]?.label ?? e.verdict }}
              </span>
            </td>
            <td class="max-sm:order-7 max-sm:basis-full">
              <div
                v-for="(r, i) in e.rules"
                :key="`${r.id}-${i}`"
                class="flex flex-wrap items-center gap-2"
              >
                <span class="badge font-mono" :title="r.data || r.message">{{ r.id }}</span>
                <span class="text-ink-muted">{{ r.message }}</span>
                <template v-if="profileOf(e.site)">
                  <template v-if="!auth.readOnly">
                    <button type="button" class="link" @click="exclude(e, r, false)">
                      Exclude
                    </button>
                    <button type="button" class="link" @click="exclude(e, r, true)">
                      Exclude on this path
                    </button>
                  </template>
                </template>
                <span v-else class="text-ink-muted">The site has no WAF profile.</span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
