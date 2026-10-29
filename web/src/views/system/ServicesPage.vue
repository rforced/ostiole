<script setup lang="ts">
import { computed, ref } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import type { ServicesReply, ServiceUnit } from '@/lib/types.gen'
import { stateWord } from '@/lib/units'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'

const REFRESH_MS = 10_000
const DAEMON = 'ostiole.service'
/** How long Ostiole gets to come back after restarting itself. */
const BACK_WITHIN_MS = 60_000

const auth = useAuthStore()
const confirm = useConfirmStore()
const reply = ref<ServicesReply | null>(null)

const load = useAsync(
  async () => {
    reply.value = await api.systemServices.list()
  },
  { immediate: true, interval: REFRESH_MS },
)

const units = computed(() => reply.value?.units ?? [])

/** The unit whose restart or start is in flight. */
const acting = ref('')

const act = useAsync(async (u: ServiceUnit, action: 'restart' | 'start') => {
  acting.value = u.unit
  try {
    const before = u.since
    const res = await api.systemServices[action](u.unit)
    if (res.error) throw new Error(`${u.name}: ${res.error}`)
    if (res.restarting) {
      // The reads would fail while it is down, and say so.
      load.stop()
      try {
        await back(before)
      } finally {
        load.start()
      }
    }
    await load.run()
  } finally {
    acting.value = ''
  }
})

const error = computed(() => load.error.value || act.error.value)

/**
 * Waits for Ostiole to answer again after restarting itself: its start
 * time moves, or, with none to compare, a read fails and one succeeds.
 */
async function back(before?: string) {
  let failed = false
  for (const end = Date.now() + BACK_WITHIN_MS; Date.now() < end;) {
    await new Promise((resolve) => setTimeout(resolve, 1000))
    try {
      const r = await api.systemServices.list()
      const since = r.units?.find((u) => u.unit === DAEMON)?.since
      if (before ? since !== undefined && since !== before : failed) return
    } catch {
      failed = true
    }
  }
  throw new Error('Ostiole did not come back within a minute.')
}

async function restart(u: ServiceUnit) {
  const ok = await confirm.ask({
    question: `Restart ${u.name}?`,
    description: u.effect ?? '',
    confirmLabel: 'Restart',
  })
  if (ok) await act.run(u, 'restart')
}

const offers = (u: ServiceUnit, action: string) => !auth.readOnly && u.actions?.includes(action)
/** Systemd has said something about it, so a count of 0 is a count. */
const known = (u: ServiceUnit) => u.state === 'active' || u.state === 'inactive'
</script>

<template>
  <SectionCard
    title="Services"
    :count="units.length"
    intro="Restarts counts how often systemd restarted a service on its own since it was last started."
    flush
  >
    <template #actions>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </template>
    <div v-if="error" class="card-strip">
      <ErrorLine>{{ error }}</ErrorLine>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Service</th>
          <th>State</th>
          <th>Running since</th>
          <th class="num">Restarts</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!units.length">
          <td colspan="5" class="text-ink-muted">{{ emptyText(load, 'No services.') }}</td>
        </tr>
        <tr v-for="u in units" :key="u.unit">
          <td data-label="">
            <div class="font-medium">{{ u.name }}</div>
            <div class="font-mono text-code break-all text-ink-muted">{{ u.unit }}</div>
          </td>
          <td data-label="State">
            <StatusBadge :state="stateWord(u)" />
            <div v-if="u.failure" class="mt-1 text-sm text-bad">{{ u.failure }}</div>
            <div v-if="u.detail && u.state !== 'active'" class="mt-1 text-sm text-ink-muted">
              {{ u.detail }}
            </div>
          </td>
          <td class="when" data-label="Running since">{{ formatWhen(u.since, '—') }}</td>
          <td class="num" data-label="Restarts">{{ known(u) ? u.restarts : '—' }}</td>
          <td class="actions" data-label="">
            <ActionButton
              v-if="offers(u, 'restart')"
              kind="link"
              label="Restart"
              busy-label="Restarting…"
              :busy="acting === u.unit"
              :disabled="acting !== ''"
              @click="restart(u)"
            />
            <ActionButton
              v-else-if="offers(u, 'start')"
              kind="link"
              label="Start"
              busy-label="Starting…"
              :busy="acting === u.unit"
              :disabled="acting !== ''"
              @click="act.run(u, 'start')"
            />
          </td>
        </tr>
      </tbody>
    </table>
    <template v-if="reply && !reply.control" #footer>
      This daemon is not running as root, so it cannot restart services.
    </template>
  </SectionCard>
</template>
