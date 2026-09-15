<script setup>
import { ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const target = ref('9.9.9.9')
const iface = ref('')
const count = ref(4)
const resolveNames = ref(true)
const busy = ref('')
const error = ref('')
const ping = ref(null)
const trace = ref(null)

async function runPing() {
  busy.value = 'ping'
  error.value = ''
  trace.value = null
  try {
    ping.value = await api.diagnostics.ping({
      target: target.value.trim(),
      interface: iface.value,
      count: Number(count.value) || 4,
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = ''
  }
}

async function runTrace() {
  busy.value = 'trace'
  error.value = ''
  ping.value = null
  try {
    trace.value = await api.diagnostics.traceroute({
      target: target.value.trim(),
      resolve: resolveNames.value,
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div class="space-y-4">
    <form class="flex flex-wrap items-end gap-4" @submit.prevent="runPing">
      <FormField id="dg-target" label="Target" hint="An address or a name this firewall resolves.">
        <input
          id="dg-target"
          v-model="target"
          class="input w-64 font-mono"
          required
          spellcheck="false"
        />
      </FormField>
      <FormField id="dg-if" label="Through interface" hint="Optional; otherwise routing decides.">
        <select id="dg-if" v-model="iface" class="input w-48">
          <option value="">Automatic</option>
          <option v-for="i in config.interfaces" :key="i.name" :value="i.name">{{ i.name }}</option>
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
      <button type="submit" class="btn-primary" :disabled="busy !== ''">
        {{ busy === 'ping' ? 'Pinging…' : 'Ping' }}
      </button>
      <button type="button" class="btn-secondary" :disabled="busy !== ''" @click="runTrace">
        {{ busy === 'trace' ? 'Tracing…' : 'Traceroute' }}
      </button>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="resolveNames" type="checkbox" class="size-4 rounded border-neutral-300" />
        Resolve hop names
      </label>
    </form>

    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <section v-if="ping" class="card" aria-label="Ping result">
      <h2 class="card-title">
        <span class="font-mono">{{ ping.address }}</span>
        <span class="text-neutral-500"> — {{ ping.received }}/{{ ping.sent }} answered</span>
      </h2>
      <dl class="kv text-sm">
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
      <ol class="mt-3 space-y-1 font-mono text-xs">
        <li v-for="p in ping.probes" :key="p.seq">
          seq {{ p.seq }}:
          <span v-if="p.error" class="text-amber-700 dark:text-amber-400">{{ p.error }}</span>
          <span v-else>{{ p.rttMs.toFixed(2) }} ms</span>
        </li>
      </ol>
    </section>

    <section v-if="trace" class="card" aria-label="Traceroute result">
      <h2 class="card-title">
        Path to <span class="font-mono">{{ trace.address }}</span>
        <span v-if="!trace.complete" class="badge badge-warn ml-2">never arrived</span>
      </h2>
      <div class="-mx-4 -mb-4 overflow-x-auto">
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
              <td class="font-mono text-xs">{{ h.ttl }}</td>
              <td class="font-mono text-xs">
                {{ h.address || '*' }}
                <span v-if="h.final" class="badge badge-ok ml-1">target</span>
              </td>
              <td class="text-xs">{{ h.name }}</td>
              <td class="text-right font-mono text-xs">
                {{ h.address ? `${h.rttMs.toFixed(1)} ms` : '' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>
