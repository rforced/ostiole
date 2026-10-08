<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { accessNames, endpoint, fwlogInfo, fwlogValues, matchedLabel } from '@/lib/fwlog'
import { heldLine, useLog } from '@/lib/log'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const access = computed(() => accessNames(config.proxy))

const management = computed(() => config.draft?.system?.management ?? {})
/** The retention, kept out of the model while every field is the default. */
const retention = computed({
  get: () => management.value.firewallLog ?? {},
  set: (v) => {
    const m = config.draft.system.management
    if (Object.keys(v).length) m.firewallLog = v
    else delete m.firewallLog
  },
})
const logDrops = computed({
  get: () => Boolean(management.value.logDefaultDrops),
  set: (on) => {
    const m = config.draft.system.management
    if (on) m.logDefaultDrops = true
    else delete m.logDefaultDrops
  },
})

const show = ref('all')
const card = ref(null)

/**
 * Blocked covers drop and reject. A packet logged by a ruleset written
 * before the verdict went into the prefix has no action to go on, so it
 * shows either way rather than being hidden on a guess, as the router
 * reads it.
 */
function chosen(e) {
  if (show.value === 'all' || !e.action) return true
  return show.value === 'blocked' ? e.action !== 'accept' : e.action === 'accept'
}

const log = useLog({
  read: async (params, signal) => {
    try {
      return await api.log.entries(params, signal)
    } catch (e) {
      if (e instanceof ApiError && e.status === 503)
        throw new Error('The firewall log needs the daemon to run as root.', { cause: e })
      throw e
    }
  },
  stream: '/api/v1/log/stream',
  filters: () => (show.value === 'all' ? {} : { show: show.value }),
  keep: chosen,
  values: (e) => fwlogValues(e, access.value),
  top: card,
})
const { rows, query, live } = log

/** Empties the router's log, its files included, and reads it again. */
const clear = useAsync(async () => {
  await api.log.clear()
  await log.reload()
})
/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)
const label = (e) => matchedLabel(e, access.value)

/** The verdict carries the colour: that a rule matched says nothing on
 *  its own about whether the packet got through. */
function actionClass(action) {
  if (action === 'accept') return 'badge-ok'
  if (action === 'reject') return 'badge-warn'
  if (action === 'drop') return 'badge-bad'
  return ''
}

const empty = computed(() => {
  if (!log.updatedAt.value) return log.reading.value ? 'Reading…' : 'No packets logged.'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return show.value === 'all' ? 'No packets logged.' : 'Nothing matches this view.'
})
</script>

<template>
  <div class="space-y-5">
    <LogRetention v-model="retention" log="firewall" title="Firewall log">
      <ToggleRow
        v-model="logDrops"
        label="Log dropped packets"
        hint="The default for every interface, covering the drops this firewall makes on its own.
          Any one interface can say otherwise under Interfaces, and any zone can under Zones. Each
          drop logs at most 10 packets a second, and the Rules page still counts every one."
      />
    </LogRetention>

    <SectionCard ref="card" title="Logs" flush>
      <template #intro>
        {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value, inFiles) : '' }}
        <template v-if="inFiles">Older ones are read from the files.</template>
        <template v-else>Kept in memory, so a restart empties it.</template>
      </template>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(error)" />
        <ClearLogButton
          name="firewall log"
          noun="packet"
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip flex flex-wrap items-center gap-3">
        <select v-model="show" class="input w-36 max-sm:w-full" aria-label="Show">
          <option value="all">All</option>
          <option value="blocked">Blocked</option>
          <option value="allowed">Allowed</option>
        </select>
        <SearchBox v-model="query" placeholder="address, port, rule, or interface" />
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      </div>

      <!-- On a phone an entry is three lines: when and what; the packet;
           the links it crossed. The row's ::before and ::after break them. -->
      <table class="table table-flow">
        <thead>
          <tr>
            <th>Time</th>
            <th>Action</th>
            <th>Matched</th>
            <th>In</th>
            <th>Out</th>
            <th>Proto</th>
            <th>Source</th>
            <th>Destination</th>
            <th>Info</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="9" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr
            v-for="e in rows"
            :key="e.seq"
            class="max-sm:before:order-4 max-sm:before:basis-full max-sm:before:content-[''] max-sm:after:order-8 max-sm:after:basis-full max-sm:after:content-['']"
          >
            <td class="when max-sm:order-1">
              {{ formatWhen(e.time) }}
            </td>
            <td class="max-sm:order-2">
              <span class="badge" :class="actionClass(e.action)">{{ e.action || 'unknown' }}</span>
            </td>
            <td class="max-sm:order-3">
              <span class="badge">{{ label(e) }}</span>
            </td>
            <td
              class="font-mono text-code max-sm:order-9 max-sm:text-ink-muted max-sm:before:content-['in_']"
            >
              {{ e.in }}
            </td>
            <td
              class="font-mono text-code max-sm:order-10 max-sm:text-ink-muted max-sm:before:content-['out_']"
            >
              {{ e.out }}
            </td>
            <td class="font-mono text-code max-sm:order-5">{{ e.proto }}</td>
            <td class="font-mono text-code max-sm:order-6">{{ endpoint(e.src, e.srcPort) }}</td>
            <td
              class="font-mono text-code max-sm:order-7 max-sm:before:mr-2 max-sm:before:content-['→']"
            >
              {{ endpoint(e.dst, e.dstPort) }}
            </td>
            <td class="font-mono text-code text-ink-muted max-sm:order-11">{{ fwlogInfo(e) }}</td>
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
