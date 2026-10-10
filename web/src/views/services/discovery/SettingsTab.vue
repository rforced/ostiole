<script setup>
import { computed, ref, watch } from 'vue'

import FormField from '@/components/FormField.vue'
import InterfaceLabel from '@/components/InterfaceLabel.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { segmentInterfaces } from '@/lib/interfaces'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const disc = computed(() => config.ensureDiscovery())

/** A field of the block, written through the store so a default is never stored. */
function field(key) {
  return computed({
    get: () => disc.value[key],
    set: (v) => config.setDiscovery({ [key]: v }),
  })
}
const mdns = field('mdns')
const ssdp = field('ssdp')

const entries = computed(() => disc.value.interfaces ?? [])
const entryOf = (name) => entries.value.find((l) => l.interface === name)

/**
 * The networks a packet can be relayed on, and any listed one that no
 * longer qualifies, so it can still be taken out.
 */
const rows = computed(() => {
  const offered = segmentInterfaces(config.draft)
  const names = new Set(offered.map((i) => i.name))
  const stale = config.interfaces.filter((i) => !names.has(i.name) && entryOf(i.name))
  return [...offered, ...stale]
})

/** A first tick adds the network in both roles; clearing both takes it out. */
function setRole(name, role, on) {
  const was = entryOf(name)
  let list
  if (!was) list = on ? [...entries.value, { interface: name, asks: true, answers: true }] : null
  else {
    const next = { ...was, [role]: on }
    list =
      next.asks || next.answers
        ? entries.value.map((l) => (l === was ? next : l))
        : entries.value.filter((l) => l !== was)
  }
  if (list) config.setDiscovery({ interfaces: list })
}

const parseTypes = (text) =>
  text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)

const typesText = ref((disc.value.services ?? []).join('\n'))
watch(
  () => disc.value.services ?? [],
  (list) => {
    if (parseTypes(typesText.value).join('\n') !== list.join('\n')) {
      typesText.value = list.join('\n')
    }
  },
)
function onTypes(text) {
  typesText.value = text
  config.setDiscovery({ services: parseTypes(text) })
}
</script>

<template>
  <SectionCard title="Settings" :locked="auth.readOnly">
    <div class="space-y-5">
      <fieldset class="field-group">
        <legend>Protocols</legend>
        <ToggleRow v-model="mdns" label="mDNS" />
        <ToggleRow v-model="ssdp" label="SSDP" />
      </fieldset>

      <fieldset class="field-group">
        <legend>Interfaces</legend>
        <p class="text-ink-muted">
          Questions go from networks that ask to networks that answer, and answers come back.
        </p>
        <table class="table">
          <thead>
            <tr>
              <th>Interface</th>
              <th>Asks</th>
              <th>Answers</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!rows.length">
              <td colspan="3" class="text-ink-muted">No interfaces.</td>
            </tr>
            <tr v-for="i in rows" :key="i.name">
              <td><InterfaceLabel :iface="i" /></td>
              <td>
                <input
                  type="checkbox"
                  class="checkbox"
                  :aria-label="`${i.name} asks`"
                  :checked="Boolean(entryOf(i.name)?.asks)"
                  @change="setRole(i.name, 'asks', $event.target.checked)"
                />
              </td>
              <td>
                <input
                  type="checkbox"
                  class="checkbox"
                  :aria-label="`${i.name} answers`"
                  :checked="Boolean(entryOf(i.name)?.answers)"
                  @change="setRole(i.name, 'answers', $event.target.checked)"
                />
              </td>
            </tr>
          </tbody>
        </table>
        <p class="text-ink-muted">
          Both are on when a network is added, so untick Asks on one whose devices must not browse
          the others.
        </p>
      </fieldset>

      <div class="fields fields-card">
        <FormField
          id="discovery-services"
          label="Service types"
          hint="One service type per line, such as _googlecast._tcp. Empty means every service."
        >
          <textarea
            id="discovery-services"
            :value="typesText"
            class="input h-32 font-mono"
            spellcheck="false"
            @input="onTypes($event.target.value)"
          ></textarea>
        </FormField>
      </div>
    </div>
    <template #footer>
      Finding a device opens nothing: a rule from the asking zone to the device's zone lets them
      talk. See
      <RouterLink to="/firewall/rules" class="link">Firewall › Rules</RouterLink>.
    </template>
  </SectionCard>
</template>
