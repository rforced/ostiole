<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { dhcpValues } from '@/lib/dhcpLog'
import { formatWhen } from '@/lib/format'
import { heldLine, useLog } from '@/lib/log'
import { records } from '@/lib/logs'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const dhcp = computed(() => config.ensureServices().dhcp)

/** The retention, kept out of the model while every field is the default. */
const retention = computed({
  get: () => dhcp.value.log ?? {},
  set: (v) => {
    if (Object.keys(v).length) dhcp.value.log = v
    else delete dhcp.value.log
  },
})

const card = ref(null)
const log = useLog({
  read: (params, signal) => api.services.dhcpLog(params, signal),
  stream: '/api/v1/dhcp/log/stream',
  values: dhcpValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the router's log, its files included, and reads it again. */
const clear = useAsync(async () => {
  await api.services.clearDhcpLog()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))
/** The level the router runs at keeps what the server says of its clients. */
const kept = computed(() => records(config.saved))
const off = computed(() => !config.saved?.services?.dhcp?.enabled)

const empty = computed(() => {
  const q = query.value.trim()
  return emptyText(log, q ? `Nothing matches "${q}".` : 'No messages.')
})
</script>

<template>
  <div class="space-y-5">
    <LogRetention
      v-model="retention"
      log="dhcp"
      title="DHCP log"
      intro="What the DHCP server says of each client, for IPv4 and IPv6."
    />

    <SectionCard ref="card" title="Log" flush>
      <template #intro>
        {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value, inFiles) : '' }}
        <template v-if="inFiles">Older ones are read from the files.</template>
        <template v-else>Kept in memory, so a restart empties it.</template>
      </template>
      <template #actions>
        <LiveButton v-model="live" :failing="Boolean(error)" />
        <ClearLogButton
          v-if="kept"
          name="DHCP log"
          noun="message"
          journal
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <SearchBox v-model="query" placeholder="message, interface, address, device, or MAC" />
        <p v-if="!kept" class="text-ink-muted">
          The DHCP log is kept at the Info and Debug log levels, under
          <RouterLink to="/system/general" class="link">System › General</RouterLink>.
        </p>
        <p v-else-if="off" class="text-ink-muted">DHCP is off, so no new messages arrive.</p>
        <ErrorLine v-if="error">{{ error }}</ErrorLine>
      </div>
      <!-- On a phone a message is two lines: when, what and where; the
           client and what it said. -->
      <table class="table table-flow">
        <thead>
          <tr>
            <th>Time</th>
            <th>Message</th>
            <th>Interface</th>
            <th>Address</th>
            <th>Client</th>
            <th>Name</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="6" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr
            v-for="e in rows"
            :key="e.seq"
            class="max-sm:after:order-4 max-sm:after:basis-full max-sm:after:content-['']"
          >
            <td class="when max-sm:order-1">
              {{ formatWhen(e.time) }}
            </td>
            <td class="whitespace-nowrap max-sm:order-2">
              <span
                class="badge"
                :class="{ 'badge-bad': e.message === 'NAK' || e.message === 'DECLINE' }"
              >
                {{ e.message }}
              </span>
            </td>
            <td class="font-mono text-code max-sm:order-3">{{ e.interface }}</td>
            <td class="font-mono text-code max-sm:order-3">{{ e.address || '—' }}</td>
            <td class="max-sm:order-5">
              <div v-if="e.device">{{ e.device }}</div>
              <div
                v-if="e.mac"
                class="flex items-center gap-1.5 font-mono text-code"
                :class="{ 'text-xs text-ink-muted': e.device }"
              >
                {{ e.mac }} <RandomMacBadge :mac="e.mac" />
              </div>
              <div
                v-else-if="e.duid"
                class="truncate font-mono text-code"
                :class="{ 'text-xs text-ink-muted': e.device }"
                :title="e.duid"
              >
                {{ e.duid }}
              </div>
            </td>
            <td class="max-sm:order-6">
              {{ e.name || '' }}
              <div v-if="e.detail" class="text-ink-muted">{{ e.detail }}</div>
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
