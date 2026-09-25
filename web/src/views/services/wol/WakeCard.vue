<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import { interfaceLabel } from '@/lib/interfaces'
import { useWake, wakeInterfaces } from '@/lib/wol'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const { busy, errors, wake } = useWake()

/**
 * The server sends on the configuration the router is running, so the
 * choice comes from the saved one: an interface only in the draft does
 * not exist yet.
 */
const choices = computed(() => wakeInterfaces(config.saved).filter((i) => i.enabled))
const iface = ref('')
const mac = ref('')

watch(
  choices,
  (list) => {
    if (!list.some((i) => i.name === iface.value)) iface.value = list[0]?.name ?? ''
  },
  { immediate: true },
)

function send() {
  const target = {
    interface: iface.value,
    mac: mac.value.trim().toLowerCase().replaceAll('-', ':'),
  }
  wake(target, `${target.mac} on ${target.interface}`, 'once')
}
</script>

<template>
  <SectionCard title="Wake a device">
    <form class="form-row" @submit.prevent="send">
      <FormField id="wol-once-mac" label="MAC address">
        <input
          id="wol-once-mac"
          v-model="mac"
          class="input w-56 font-mono max-sm:w-full"
          placeholder="aa:bb:cc:dd:ee:ff"
          required
          spellcheck="false"
        />
      </FormField>
      <FormField id="wol-once-if" label="Interface">
        <select id="wol-once-if" v-model="iface" class="input w-48 max-sm:w-full" required>
          <option v-if="!choices.length" value="">None applied yet</option>
          <option v-for="i in choices" :key="i.name" :value="i.name">
            {{ interfaceLabel(i) }}
          </option>
        </select>
      </FormField>
      <button
        type="submit"
        class="btn-primary"
        :disabled="busy !== '' || !iface"
        :aria-busy="busy === 'once'"
      >
        <LoaderCircle v-if="busy === 'once'" class="size-4 animate-spin" aria-hidden="true" />
        {{ busy === 'once' ? 'Waking…' : 'Wake' }}
      </button>
    </form>
    <p v-if="errors.length" role="alert" class="mt-3 text-bad">{{ errors[0] }}</p>
  </SectionCard>
</template>
