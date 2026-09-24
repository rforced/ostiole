<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatOffset } from '@/lib/format'
import { SOURCE_WORDS, sourcesFor, useNtpStatus } from '@/lib/ntpStatus'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ServerDialog from '@/views/services/time/ServerDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const { status } = useNtpStatus()

/** The router's own list. Empty means it asks the defaults. */
const own = computed(() => config.ntp.servers ?? [])
const usingDefaults = computed(() => !own.value.length)
/** The list the router asks, which is the defaults until it has its own. */
const servers = computed(() => (usingDefaults.value ? (status.value?.defaults ?? []) : own.value))

const same = (a, b) => a.toLowerCase() === b.toLowerCase()

/** How the best source a server gave is doing, once the service was read. */
function stateOf(server) {
  if (!status.value?.read) return ''
  const best = sourcesFor(server.host, status.value.sources)[0]
  return best ? (SOURCE_WORDS[best.state] ?? best.state) : 'no answer'
}

function offsetOf(server) {
  if (!status.value?.read) return ''
  const best = sourcesFor(server.host, status.value.sources)[0]
  return best && best.lastSeconds >= 0 ? formatOffset(best.offsetSeconds) : ''
}

/** Whether a server asked to sign its answers is failing to. */
function failing(server) {
  return (
    server.nts && sourcesFor(server.host, status.value?.sources).some((s) => s.nts === 'failing')
  )
}

const open = ref(false)
/** The server the dialog edits; null adds one. */
const editing = ref(null)

function add() {
  editing.value = null
  open.value = true
}
function edit(server) {
  editing.value = server
  open.value = true
}

/**
 * The defaults become the draft's own list at the first edit, so a server
 * added beside them keeps them.
 *
 * @param {{host: string, nts?: boolean, pool?: boolean}} server
 * @param {string} previousHost the host it was saved under, empty for a new one
 */
function save(server, previousHost) {
  const list = servers.value.map((s) => ({ ...s }))
  const idx = previousHost ? list.findIndex((s) => same(s.host, previousHost)) : -1
  if (idx === -1) list.push(server)
  else list[idx] = server
  config.setNTP({ servers: list })
}

function remove(server) {
  const list = servers.value.filter((s) => !same(s.host, server.host))
  config.undoable(`Removed ${server.host}.`, () => config.setNTP({ servers: list }))
}

function useDefaults() {
  config.undoable('Back to the default servers.', () => config.setNTP({ servers: [] }))
}
</script>

<template>
  <SectionCard
    title="Time servers"
    :count="servers.length"
    :intro="usingDefaults ? `These follow Ostiole's defaults until you edit the list.` : ''"
    flush
  >
    <template v-if="!auth.readOnly" #actions>
      <button v-if="!usingDefaults" type="button" class="btn-secondary" @click="useDefaults">
        Use defaults
      </button>
      <button type="button" class="btn-secondary" :disabled="usingDefaults && !status" @click="add">
        <Plus class="size-4" aria-hidden="true" /> Add server
      </button>
    </template>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Server</th>
          <th>Answers</th>
          <th>State</th>
          <th>Offset</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!servers.length">
          <td colspan="5" class="text-ink-muted">Reading…</td>
        </tr>
        <tr
          v-for="(s, i) in servers"
          :key="s.host"
          :class="{ 'row-changed': !usingDefaults && config.isChanged('services.ntp.servers', i) }"
        >
          <td class="font-mono text-code" data-label="Server">
            {{ s.host }}<span v-if="s.pool" class="badge ml-2">pool</span>
          </td>
          <td data-label="Answers">
            <StatusBadge v-if="failing(s)" state="failing" />
            <template v-else>{{ s.nts ? 'Signed' : 'Unsigned' }}</template>
          </td>
          <td data-label="State">
            <StatusBadge v-if="stateOf(s)" :state="stateOf(s)" />
          </td>
          <td data-label="Offset">{{ offsetOf(s) }}</td>
          <td class="text-right whitespace-nowrap" data-label="">
            <button type="button" class="link" @click="edit(s)">
              {{ auth.readOnly ? 'View' : 'Edit' }}
            </button>
            <ConfirmButton
              class="ml-3"
              label="Delete"
              :question="`Delete the time server ${s.host}?`"
              @confirm="remove(s)"
            />
          </td>
        </tr>
      </tbody>
    </table>
  </SectionCard>

  <ServerDialog
    v-model:open="open"
    :server="editing"
    :hosts="servers.map((s) => s.host)"
    @save="save"
  />
</template>
