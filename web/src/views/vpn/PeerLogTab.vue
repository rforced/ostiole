<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { heldLine, useLog } from '@/lib/log'
import { records } from '@/lib/logs'
import { peerValues } from '@/lib/peerLog'
import { useConfigStore } from '@/stores/config'

/**
 * A VPN's peers coming and going, as the router logs them at the Info and
 * Debug levels: WireGuard's or Tailscale's.
 */
const props = defineProps({
  /** wireguard or tailscale */
  kind: { type: String, required: true },
})

const KINDS = {
  wireguard: {
    title: 'WireGuard log',
    setting: 'wireguardLog',
    intro:
      'Each peer connecting, going quiet after three minutes without a handshake, and roaming.',
    read: (params, signal) => api.wireguard.log(params, signal),
    clear: () => api.wireguard.clearLog(),
    on: (cfg) => (cfg?.interfaces ?? []).some((i) => i.enabled && i.wireguard),
    off: 'No tunnel is on, so no peers arrive.',
    placeholder: 'event, tunnel, peer, or endpoint',
    where: 'Endpoint',
  },
  tailscale: {
    title: 'Tailscale log',
    setting: 'tailscaleLog',
    intro: 'Each peer going online and offline, and the way its traffic goes.',
    read: (params, signal) => api.tailscale.log(params, signal),
    clear: () => api.tailscale.clearLog(),
    on: (cfg) => (cfg?.interfaces ?? []).some((i) => i.enabled && i.tailscale),
    off: 'Tailscale is off, so no peers arrive.',
    placeholder: 'event, peer, or path',
    where: 'Path',
  },
}
const spec = KINDS[props.kind]

const config = useConfigStore()

/**
 * The retention, kept out of the model while every field is the default:
 * the vpn block itself goes when nothing is left in it.
 */
const retention = computed({
  get: () => config.draft?.vpn?.[spec.setting] ?? {},
  set: (v) => {
    const vpn = { ...(config.draft.vpn ?? {}) }
    if (Object.keys(v).length) vpn[spec.setting] = v
    else delete vpn[spec.setting]
    if (Object.keys(vpn).length) config.draft.vpn = vpn
    else delete config.draft.vpn
  },
})

const card = ref(null)
const log = useLog({
  read: spec.read,
  stream: `/api/v1/${props.kind}/log/stream`,
  values: peerValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the router's log, its files included, and reads it again. */
const clear = useAsync(async () => {
  await spec.clear()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))
/** The level the router runs at keeps the peers' coming and going. */
const kept = computed(() => records(config.saved))
const off = computed(() => !spec.on(config.saved))

const empty = computed(() => {
  const q = query.value.trim()
  return emptyText(log, q ? `Nothing matches "${q}".` : 'No events.')
})

const QUIET = ['quiet', 'offline']
</script>

<template>
  <div class="space-y-5">
    <LogRetention v-model="retention" :log="kind" :title="spec.title" :intro="spec.intro" />

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
          :name="spec.title"
          noun="event"
          :busy="clear.busy.value"
          @confirm="clear.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <SearchBox v-model="query" :placeholder="spec.placeholder" />
        <p v-if="!kept" class="text-ink-muted">
          The {{ spec.title }} is kept at the Info and Debug log levels, under System, General.
        </p>
        <p v-else-if="off" class="text-ink-muted">{{ spec.off }}</p>
        <ErrorLine v-if="error">{{ error }}</ErrorLine>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Time</th>
            <th>Event</th>
            <th v-if="kind === 'wireguard'">Tunnel</th>
            <th>Peer</th>
            <th>{{ spec.where }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td :colspan="kind === 'wireguard' ? 5 : 4" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr v-for="e in rows" :key="e.seq">
            <td data-label="Time" class="when">
              {{ formatWhen(e.time) }}
            </td>
            <td data-label="Event" class="whitespace-nowrap">
              <span class="badge" :class="{ 'badge-warn': QUIET.includes(e.event) }">{{
                e.event
              }}</span>
            </td>
            <td v-if="kind === 'wireguard'" data-label="Tunnel" class="font-mono text-code">
              {{ e.tunnel }}
            </td>
            <td data-label="Peer">{{ e.peer }}</td>
            <td :data-label="spec.where" class="font-mono text-code">{{ e.endpoint || '—' }}</td>
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
