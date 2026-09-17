<script setup>
import { RefreshCw } from 'lucide-vue-next'
import { computed, onMounted, onUnmounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { formatBytes } from '@/lib/format'

const result = ref(null)
const error = ref('')
const busy = ref(false)
const auto = ref(false)
const filter = ref({ address: '', protocol: '', port: '' })
let timer = null

onMounted(load)
onUnmounted(() => clearInterval(timer))

async function load() {
  busy.value = true
  error.value = ''
  try {
    result.value = await api.diagnostics.states({
      address: filter.value.address.trim(),
      protocol: filter.value.protocol,
      port: filter.value.port,
    })
  } catch (e) {
    result.value = null
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

function toggleAuto() {
  auto.value = !auto.value
  clearInterval(timer)
  if (auto.value) timer = setInterval(load, 5000)
}

/** Protocol counts across the whole table, not just the rows shown. */
const protocols = computed(() =>
  Object.entries(result.value?.byProtocol ?? {}).sort((a, b) => b[1] - a[1]),
)

const trimmed = computed(() => result.value && result.value.matched > result.value.states.length)

function endpoint(address, port) {
  if (!address) return '—'
  return port ? `${address}:${port}` : address
}
</script>

<template>
  <div class="space-y-3">
    <p class="max-w-3xl text-sm text-neutral-500">
      Every connection the kernel is tracking. This is what the firewall's "established" rules match
      against, so it answers both "why is this getting through" and "what is this box talking to".
      Connections that are being translated are marked, with the address they leave as.
    </p>

    <form class="flex flex-wrap items-end gap-3" @submit.prevent="load">
      <FormField id="st-address" label="Address" hint="An address, or a network like 10.0.0.0/8.">
        <input
          id="st-address"
          v-model="filter.address"
          class="input w-56 font-mono"
          spellcheck="false"
          placeholder="any"
        />
      </FormField>
      <FormField id="st-proto" label="Protocol">
        <select id="st-proto" v-model="filter.protocol" class="input w-32">
          <option value="">Any</option>
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
          <option value="icmp">ICMP</option>
          <option value="icmpv6">ICMPv6</option>
        </select>
      </FormField>
      <FormField id="st-port" label="Port">
        <input
          id="st-port"
          v-model="filter.port"
          type="number"
          min="1"
          max="65535"
          class="input w-28 font-mono"
        />
      </FormField>
      <button type="submit" class="btn-primary" :disabled="busy">
        <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Refresh
      </button>
      <label class="flex items-center gap-2 pb-2 text-sm">
        <input
          type="checkbox"
          class="size-4 rounded border-neutral-300"
          :checked="auto"
          @change="toggleAuto"
        />
        Every 5 seconds
      </label>
    </form>

    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <template v-if="result">
      <p class="text-sm text-neutral-500">
        {{ result.total }} connection(s) tracked<span v-if="result.matched !== result.total">
          · {{ result.matched }} match this filter</span
        ><span v-if="trimmed"> · showing the {{ result.states.length }} busiest</span>
        <template v-if="protocols.length">
          ·
          <span v-for="([name, count], i) in protocols" :key="name">
            <span v-if="i">, </span>{{ count }} {{ name }}
          </span>
        </template>
      </p>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Protocol</th>
              <th>From</th>
              <th>To</th>
              <th>Leaves as</th>
              <th>State</th>
              <th>Traffic</th>
              <th>Expires in</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!result.states.length">
              <td colspan="7" class="text-neutral-500">Nothing matches.</td>
            </tr>
            <tr v-for="(s, i) in result.states" :key="i">
              <td class="font-mono text-xs">{{ s.protocol }}</td>
              <td class="font-mono text-xs">{{ endpoint(s.source, s.sourcePort) }}</td>
              <td class="font-mono text-xs">{{ endpoint(s.destination, s.destPort) }}</td>
              <td class="font-mono text-xs">
                <template v-if="s.nat">
                  {{ endpoint(s.replyDest, s.replyDestPort) }}
                  <span class="badge">NAT</span>
                </template>
                <span v-else class="text-neutral-500">not translated</span>
              </td>
              <td class="font-mono text-xs">
                {{ s.state || '—'
                }}<span v-if="s.mark" class="ml-1 badge" :title="`Packet mark ${s.mark}`">
                  routed
                </span>
              </td>
              <td class="font-mono text-xs whitespace-nowrap">
                {{ formatBytes(s.bytes) }} · {{ s.packets }}p
              </td>
              <td class="font-mono text-xs">{{ s.ttl }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>
