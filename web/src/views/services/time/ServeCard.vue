<script setup>
import { computed } from 'vue'

import AppDisclosure from '@/components/AppDisclosure.vue'
import InterfaceLabel from '@/components/InterfaceLabel.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { internalInterfaces } from '@/lib/interfaces'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()

const serve = computed({
  get: () => Boolean(config.ntp.serve),
  set: (v) => config.setNTP({ serve: v }),
})

/** Where time is answered with none picked, and the only ones that may be. */
const inside = computed(() => internalInterfaces(config.draft))

const answerAll = computed({
  get: () => !(config.ntp.interfaces ?? []).length,
  set: (all) => config.setNTP({ interfaces: all ? [] : inside.value.map((i) => i.name) }),
})

function toggleInterface(name, on) {
  const list = new Set(config.ntp.interfaces ?? [])
  if (on) list.add(name)
  else list.delete(name)
  config.setNTP({ interfaces: [...list] })
}
</script>

<template>
  <SectionCard title="Serving" :locked="auth.readOnly">
    <div class="space-y-4">
      <ToggleRow
        id="ntp-serve"
        v-model="serve"
        label="Answer time requests"
        hint="DHCP clients are given this router as their time server."
      />
      <AppDisclosure>
        <fieldset class="space-y-2">
          <legend class="group-title mb-2">Answer on</legend>
          <ToggleRow v-model="answerAll" label="Every interface outside external zones" />
          <ul v-if="answerAll" class="ml-6 flex flex-wrap gap-4" aria-label="Answering on">
            <li v-if="!inside.length" class="text-ink-muted">
              No enabled interface is in an internal zone yet.
            </li>
            <li v-for="i in inside" :key="i.name"><InterfaceLabel :iface="i" /></li>
          </ul>
          <div v-else class="ml-6 flex flex-wrap gap-4">
            <label v-for="i in inside" :key="i.name" class="flex items-center gap-2">
              <input
                type="checkbox"
                class="size-4 rounded"
                :checked="(config.ntp.interfaces ?? []).includes(i.name)"
                @change="toggleInterface(i.name, $event.target.checked)"
              />
              <InterfaceLabel :iface="i" />
            </label>
          </div>
        </fieldset>
      </AppDisclosure>
    </div>
  </SectionCard>
</template>
