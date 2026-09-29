<script setup>
import { computed, ref } from 'vue'

import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { heldLine, useLog } from '@/lib/log'
import { records } from '@/lib/logs'
import { wirelessValues } from '@/lib/wirelessLog'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()

/**
 * The retention, kept out of the model while every field is the default:
 * the wireless block itself goes when nothing is left in it.
 */
const retention = computed({
  get: () => config.draft?.wireless?.log ?? {},
  set: (v) => {
    const w = { ...(config.draft.wireless ?? {}) }
    if (Object.keys(v).length) w.log = v
    else delete w.log
    if (Object.keys(w).length) config.draft.wireless = w
    else delete config.draft.wireless
  },
})

const card = ref(null)
const log = useLog({
  read: (params, signal) => api.wireless.log(params, signal),
  stream: '/api/v1/wireless/log/stream',
  values: wirelessValues,
  top: card,
})
const { rows, query, live } = log

const error = computed(() => log.error.value || log.streamError.value)

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))
/** The level the router runs at keeps the clients' coming and going. */
const kept = computed(() => records(config.saved))
const off = computed(() => !(config.saved?.wireless?.radios ?? []).some((r) => r.enabled))

const empty = computed(() => {
  if (!log.updatedAt.value) return log.reading.value ? 'Reading…' : 'No clients yet.'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return 'No clients yet.'
})
</script>

<template>
  <div class="space-y-5">
    <LogRetention
      v-model="retention"
      log="wireless"
      title="Wireless log"
      intro="Each client joining and leaving a network, and each wrong password."
    />

    <SectionCard ref="card" title="Log" flush>
      <template #intro>
        {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value, inFiles) : '' }}
        <template v-if="inFiles">Older ones are read from the files.</template>
        <template v-else>Kept in memory, so a restart empties it.</template>
      </template>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(error)" />
      </template>
      <div class="card-strip space-y-2">
        <SearchBox v-model="query" placeholder="event, network, device, or MAC" />
        <p v-if="!kept" class="text-ink-muted">
          The wireless log is kept at the Info and Debug log levels, under System, General.
        </p>
        <p v-else-if="off" class="text-ink-muted">No radio is on, so no clients arrive.</p>
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Time</th>
            <th>Event</th>
            <th>Network</th>
            <th>Client</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="4" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr v-for="e in rows" :key="e.seq">
            <td data-label="Time" class="text-xs whitespace-nowrap text-ink-muted tabular-nums">
              {{ new Date(e.time).toLocaleString() }}
            </td>
            <td data-label="Event" class="whitespace-nowrap">
              <span class="badge" :class="{ 'badge-bad': e.event === 'wrong password' }">
                {{ e.event }}
              </span>
            </td>
            <td data-label="Network">
              {{ e.network || e.interface }}
              <div v-if="e.network" class="font-mono text-xs text-ink-muted">{{ e.interface }}</div>
            </td>
            <td data-label="Client">
              <div v-if="e.device">{{ e.device }}</div>
              <div
                class="flex items-center gap-1 font-mono"
                :class="e.device ? 'text-xs text-ink-muted' : 'text-code'"
              >
                {{ e.mac }} <RandomMacBadge :mac="e.mac" />
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <LoadMore
        :more="log.more.value"
        :busy="log.readingMore.value"
        :rows="rows.length"
        :searched-to="log.searchedTo.value"
        @load="log.loadMore()"
      />
    </SectionCard>
  </div>
</template>
