<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { ref, watch } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes } from '@/lib/format'
import { interfaceLabel } from '@/lib/interfaces'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useToastStore } from '@/stores/toast'

const auth = useAuthStore()
const config = useConfigStore()
const toast = useToastStore()
const iface = ref('')
// The page can mount before the configuration has arrived, so the default
// interface follows the list rather than reading it once.
watch(
  () => config.interfaces,
  (list) => {
    if (!iface.value && list.length) iface.value = list[0].name
  },
  { immediate: true },
)
const seconds = ref(10)
const count = ref(200)
const address = ref('')
const port = ref('')

const capture = useAsync(async () => {
  const { blob, name } = await api.diagnostics.capture({
    interface: iface.value,
    seconds: Number(seconds.value) || 10,
    count: Number(count.value) || 0,
    address: address.value.trim(),
    port: Number(port.value) || 0,
  })
  // Hand the file to the browser: the API is behind a CSRF header, so a
  // plain download link cannot fetch it.
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
  toast.show(`Downloaded ${name} (${formatBytes(blob.size)}).`)
})
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Packet capture"
      intro="Records the interface into a pcap file until whichever limit comes first."
    >
      <p v-if="auth.readOnly" class="text-ink-muted">Only an operator or an admin can capture.</p>
      <form v-else class="form-row" @submit.prevent="capture.run()">
        <FormField id="cp-if" label="Interface">
          <select id="cp-if" v-model="iface" class="input w-48" required>
            <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
              {{ interfaceLabel(i) }}
            </option>
          </select>
        </FormField>
        <FormField id="cp-secs" label="Seconds">
          <input
            id="cp-secs"
            v-model="seconds"
            type="number"
            min="1"
            max="120"
            class="input w-24 font-mono"
          />
        </FormField>
        <FormField id="cp-count" label="Packets">
          <input
            id="cp-count"
            v-model="count"
            type="number"
            min="1"
            max="5000"
            class="input w-28 font-mono"
          />
        </FormField>
        <FormField
          id="cp-addr"
          label="Address"
          hint="Matches source or destination. Empty for all."
        >
          <input id="cp-addr" v-model="address" class="input w-44 font-mono" spellcheck="false" />
        </FormField>
        <FormField id="cp-port" label="Port" hint="Optional.">
          <input
            id="cp-port"
            v-model="port"
            type="number"
            min="1"
            max="65535"
            class="input w-28 font-mono"
          />
        </FormField>
        <button
          type="submit"
          class="btn-primary"
          :disabled="capture.busy.value || !iface"
          :aria-busy="capture.busy.value"
        >
          <LoaderCircle v-if="capture.busy.value" class="size-4 animate-spin" aria-hidden="true" />
          {{ capture.busy.value ? 'Capturing…' : 'Capture' }}
        </button>
      </form>
      <p v-if="capture.error.value" role="alert" class="mt-3 text-bad">
        {{ capture.error.value }}
      </p>
    </SectionCard>
  </div>
</template>
