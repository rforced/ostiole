<script setup>
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

const STORAGE_KEY = 'ostiole.modem.address'
const DEFAULT_ADDRESS = '192.168.100.1'

const address = ref(localStorage.getItem(STORAGE_KEY) || DEFAULT_ADDRESS)
const status = ref(null)

const load = useAsync(async (refresh = false) => {
  const target = address.value.trim() || DEFAULT_ADDRESS
  address.value = target
  if (target === DEFAULT_ADDRESS) localStorage.removeItem(STORAGE_KEY)
  else localStorage.setItem(STORAGE_KEY, target)
  status.value = await api.diagnostics.modem(target, refresh)
})
// Reading the modem is an operator's: it has the router reach out.
onMounted(() => {
  if (!auth.readOnly) load.run(false)
})

const stuckAt = computed(() => status.value?.provisioning.find((s) => !s.ok) ?? null)

const mhz = (hz) => (hz ? `${(hz / 1e6).toFixed(hz % 1e6 ? 1 : 0)} MHz` : '—')
const dbmv = (v) => `${v.toFixed(1)} dBmV`
const db = (v) => `${v.toFixed(1)} dB`
const count = (n) => n.toLocaleString()

/**
 * DOCSIS levels as the cable industry quotes them: downstream within
 * ±7 dBmV and SNR of 33 dB or better for QAM256; upstream 35–49 dBmV.
 * A little outside is worth a look, further out is a fault.
 */
function downPowerTone(p) {
  const a = Math.abs(p)
  return a <= 7 ? 'badge-ok' : a <= 10 ? 'badge-warn' : 'badge-bad'
}
function snrTone(snr) {
  return snr >= 33 ? 'badge-ok' : snr >= 30 ? 'badge-warn' : 'badge-bad'
}
function upPowerTone(p) {
  if (p >= 35 && p <= 49) return 'badge-ok'
  return p >= 30 && p <= 51 ? 'badge-warn' : 'badge-bad'
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Modem">
      <template #intro>
        Read from the modem at 192.168.100.1 when you ask, never in the background. It answers there
        with or without a WAN address.
      </template>
      <template v-if="!auth.readOnly" #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run(true)"
        />
      </template>
      <p v-if="auth.readOnly" class="text-ink-muted">
        Only an operator or an admin can read the modem.
      </p>
      <div v-else class="space-y-3">
        <form
          class="form-row"
          @submit.prevent="load.run(true)"
          @keydown.enter.prevent="load.run(true)"
        >
          <FormField
            id="modem-address"
            label="Modem address"
            hint="In 192.168.100.0/24, where cable modems answer."
          >
            <input
              id="modem-address"
              v-model="address"
              class="input w-48 font-mono"
              placeholder="192.168.100.1"
              spellcheck="false"
            />
          </FormField>
        </form>
        <p v-if="load.error.value" role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
    </SectionCard>

    <template v-if="status">
      <div class="grid gap-5 md:grid-cols-2">
        <SectionCard :title="`${status.vendor} ${status.model}`">
          <dl class="kv">
            <dt>Firmware</dt>
            <dd class="font-mono text-code">{{ status.firmware || '—' }}</dd>
            <dt>Hardware</dt>
            <dd class="font-mono text-code">{{ status.hardware || '—' }}</dd>
            <dt>Serial</dt>
            <dd class="font-mono text-code">{{ status.serial || '—' }}</dd>
            <dt>Cable MAC</dt>
            <dd class="font-mono text-code">{{ status.mac || '—' }}</dd>
            <dt>Uptime</dt>
            <dd>{{ status.uptime || '—' }}</dd>
            <dt>Modem clock</dt>
            <dd>{{ status.clock || '—' }}</dd>
            <dt>Link to router</dt>
            <dd>
              <span class="badge" :class="status.link.up ? 'badge-ok' : 'badge-bad'">
                {{ status.link.up ? 'up' : 'down' }}
              </span>
              <span v-if="status.link.speed" class="ml-2">
                {{ status.link.speed }}
                <span v-if="status.link.duplex" class="text-ink-muted">
                  {{ status.link.duplex.toLowerCase() }} duplex
                </span>
              </span>
            </dd>
          </dl>
        </SectionCard>

        <SectionCard title="Provisioning">
          <div class="space-y-2">
            <p v-if="stuckAt" class="text-ink-muted">
              Stopped at <strong>{{ stuckAt.name }}</strong
              >: everything after it waits on that.
            </p>
            <p v-else-if="status.provisioning.length" class="text-ink-muted">
              Every step passed. If the WAN still has no address, the provider's DHCP is next to
              ask.
            </p>
            <p v-else class="text-ink-muted">The modem did not report its steps.</p>
            <ol class="space-y-1">
              <li
                v-for="s in status.provisioning"
                :key="s.name"
                class="flex items-center justify-between gap-3"
              >
                <span>{{ s.name }}</span>
                <span class="badge" :class="s.ok ? 'badge-ok' : 'badge-bad'">{{ s.status }}</span>
              </li>
            </ol>
          </div>
        </SectionCard>
      </div>

      <SectionCard
        title="Downstream"
        :count="status.downstream.length"
        intro="A receive level within ±7 dBmV and an SNR of 33 dB or better is healthy."
        flush
      >
        <table class="table">
          <thead>
            <tr>
              <th>Channel</th>
              <th>Frequency</th>
              <th>Modulation</th>
              <th>Power</th>
              <th>SNR</th>
              <th class="text-right">Corrected</th>
              <th class="text-right">Uncorrectable</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!status.downstream.length">
              <td colspan="7" class="text-ink-muted">No downstream channel is locked.</td>
            </tr>
            <tr v-for="d in status.downstream" :key="`${d.kind}-${d.channel}`">
              <td class="font-mono text-code">
                {{ d.channel }}
                <span v-if="d.kind === 'ofdm'" class="badge ml-1">OFDM</span>
                <span v-if="!d.locked" class="badge badge-bad ml-1">not locked</span>
              </td>
              <td class="font-mono text-code">{{ mhz(d.frequency) }}</td>
              <td>{{ d.modulation || '—' }}</td>
              <td>
                <span class="badge" :class="downPowerTone(d.power)">{{ dbmv(d.power) }}</span>
              </td>
              <td>
                <span class="badge" :class="snrTone(d.snr)">{{ db(d.snr) }}</span>
              </td>
              <td class="text-right font-mono text-code">{{ count(d.corrected) }}</td>
              <td class="text-right font-mono text-code">
                <span :class="d.uncorrectable ? 'text-bad' : ''">
                  {{ count(d.uncorrectable) }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </SectionCard>

      <SectionCard
        title="Upstream"
        :count="status.upstream.length"
        intro="A transmit level of 35 to 49 dBmV is healthy."
        flush
      >
        <table class="table">
          <thead>
            <tr>
              <th>Channel</th>
              <th>Frequency</th>
              <th>Width</th>
              <th>Modulation</th>
              <th>Power</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!status.upstream.length">
              <td colspan="5" class="text-ink-muted">No upstream channel is ranged.</td>
            </tr>
            <tr v-for="u in status.upstream" :key="`${u.kind}-${u.channel}`">
              <td class="font-mono text-code">
                {{ u.channel }}
                <span v-if="u.kind === 'ofdma'" class="badge ml-1">OFDMA</span>
              </td>
              <td class="font-mono text-code">{{ mhz(u.frequency) }}</td>
              <td class="font-mono text-code">{{ mhz(u.bandwidth) }}</td>
              <td>
                {{ u.modulation || '—' }}
                <span v-if="u.mode" class="text-ink-muted">{{ u.mode }}</span>
              </td>
              <td>
                <span class="badge" :class="upPowerTone(u.power)">{{ dbmv(u.power) }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </SectionCard>
    </template>
  </div>
</template>
