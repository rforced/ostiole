<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { ref } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { interfaceLabel } from '@/lib/interfaces'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const target = ref('9.9.9.9')
const iface = ref('')
const count = ref(4)
const resolveNames = ref(true)
/** Which of the two is running: 'ping', 'trace', or nothing. */
const busy = ref('')
const ping = ref(null)
const trace = ref(null)

// One loader for both, so a trace clears a ping's error and the reverse.
const job = useAsync(async (kind) => {
  if (kind === 'ping') {
    trace.value = null
    ping.value = await api.diagnostics.ping({
      target: target.value.trim(),
      interface: iface.value,
      count: Number(count.value) || 4,
    })
  } else {
    ping.value = null
    trace.value = await api.diagnostics.traceroute({
      target: target.value.trim(),
      resolve: resolveNames.value,
    })
  }
})

async function run(kind) {
  busy.value = kind
  try {
    await job.run(kind)
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Ping and traceroute">
      <p v-if="auth.readOnly" class="text-ink-muted">Only an operator or an admin can run these.</p>
      <form v-else class="form-row" @submit.prevent="run('ping')">
        <FormField
          id="dg-target"
          label="Target"
          hint="An address or a name this firewall resolves."
        >
          <input
            id="dg-target"
            v-model="target"
            class="input w-64 font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="dg-if" label="Through interface" hint="Empty lets routing decide.">
          <select id="dg-if" v-model="iface" class="input w-48">
            <option value="">Automatic</option>
            <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
              {{ interfaceLabel(i) }}
            </option>
          </select>
        </FormField>
        <FormField id="dg-count" label="Probes">
          <input
            id="dg-count"
            v-model="count"
            type="number"
            min="1"
            max="20"
            class="input w-24 font-mono"
          />
        </FormField>
        <button
          type="submit"
          class="btn-primary"
          :disabled="busy !== ''"
          :aria-busy="busy === 'ping'"
        >
          <LoaderCircle v-if="busy === 'ping'" class="size-4 animate-spin" aria-hidden="true" />
          {{ busy === 'ping' ? 'Pinging…' : 'Ping' }}
        </button>
        <button
          type="button"
          class="btn-secondary"
          :disabled="busy !== ''"
          :aria-busy="busy === 'trace'"
          @click="run('trace')"
        >
          <LoaderCircle v-if="busy === 'trace'" class="size-4 animate-spin" aria-hidden="true" />
          {{ busy === 'trace' ? 'Tracing…' : 'Traceroute' }}
        </button>
        <ToggleRow v-model="resolveNames" label="Resolve hop names" />
      </form>
      <p v-if="job.error.value" role="alert" class="mt-3 text-bad">{{ job.error.value }}</p>
    </SectionCard>

    <SectionCard v-if="ping">
      <template #title>
        <span class="font-mono">{{ ping.address }}</span>
        <span class="font-normal text-ink-muted">
          · {{ ping.received }}/{{ ping.sent }} answered
        </span>
      </template>
      <dl class="kv">
        <dt>Loss</dt>
        <dd>{{ ping.lossPercent.toFixed(0) }}%</dd>
        <template v-if="ping.received">
          <dt>Round trip</dt>
          <dd class="font-mono">
            min {{ ping.minMs.toFixed(1) }} · avg {{ ping.avgMs.toFixed(1) }} · max
            {{ ping.maxMs.toFixed(1) }} ms
          </dd>
        </template>
      </dl>
      <ol class="mt-3 space-y-1 font-mono text-code">
        <li v-for="p in ping.probes" :key="p.seq">
          seq {{ p.seq }}:
          <span v-if="p.error" class="text-warn">{{ p.error }}</span>
          <span v-else>{{ p.rttMs.toFixed(2) }} ms</span>
        </li>
      </ol>
    </SectionCard>

    <SectionCard v-if="trace" flush>
      <template #title>
        Path to <span class="font-mono">{{ trace.address }}</span>
        <span v-if="!trace.complete" class="badge badge-warn">never arrived</span>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Hop</th>
            <th>Address</th>
            <th>Name</th>
            <th class="text-right">Round trip</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="h in trace.hops" :key="h.ttl">
            <td class="font-mono text-code">{{ h.ttl }}</td>
            <td class="font-mono text-code">
              {{ h.address || '*' }}
              <span v-if="h.final" class="badge badge-ok ml-1">target</span>
            </td>
            <td class="font-mono text-code">{{ h.name }}</td>
            <td class="text-right font-mono text-code">
              {{ h.address ? `${h.rttMs.toFixed(1)} ms` : '' }}
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
