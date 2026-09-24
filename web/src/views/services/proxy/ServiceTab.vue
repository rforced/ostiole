<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const proxy = computed(() => config.proxy)

function numberField(key, fallback) {
  return computed({
    get: () => proxy.value[key] || fallback,
    set: (v) => {
      const n = Number(v)
      config.setProxy({ [key]: n && n !== fallback ? n : undefined })
    },
  })
}

const httpPort = numberField('httpPort', 80)
const httpsPort = numberField('httpsPort', 443)

function toggleZone(name, on) {
  const list = new Set(proxy.value.zones ?? [])
  if (on) list.add(name)
  else list.delete(name)
  config.setProxy({ zones: [...list] })
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Service" :locked="auth.readOnly">
      <div class="space-y-5">
        <fieldset class="space-y-2">
          <legend class="group-title">Open on</legend>
          <div class="flex flex-wrap gap-4">
            <label v-for="z in config.zones" :key="z.name" class="flex items-center gap-2">
              <input
                type="checkbox"
                class="size-4 rounded"
                :checked="(proxy.zones ?? []).includes(z.name)"
                @change="toggleZone(z.name, $event.target.checked)"
              />
              <span class="font-mono">{{ z.name }}</span>
            </label>
          </div>
          <p class="text-ink-muted">Nothing reaches the proxy until a zone is ticked.</p>
        </fieldset>

        <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
          <FormField id="proxy-http" label="HTTP port" hint="80.">
            <input
              id="proxy-http"
              v-model.number="httpPort"
              type="number"
              min="1"
              max="65535"
              class="input"
            />
          </FormField>
          <FormField id="proxy-https" label="HTTPS port" hint="443.">
            <input
              id="proxy-https"
              v-model.number="httpsPort"
              type="number"
              min="1"
              max="65535"
              class="input"
            />
          </FormField>
        </div>

        <ToggleRow
          id="proxy-http3"
          :model-value="proxy.http3 === true"
          label="HTTP/3"
          hint="UDP on the HTTPS port as well."
          @update:model-value="config.setProxy({ http3: $event })"
        />
      </div>
    </SectionCard>
  </div>
</template>
