<script setup>
import { computed, ref, watch } from 'vue'

import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

/**
 * The zone an interface is in, with "New zone…" to make one on the spot.
 * A zone is the unit firewall rules match on, so interfaces sharing one
 * share a single rule list. That is worth saying out loud here: a VLAN
 * dropped into "lan" silently inherits the LAN rules and can never have its
 * own, which is rarely what someone adding a guest or IOT VLAN wants.
 *
 * It renders two blocks for a two-column grid: the select, and under it,
 * across both columns, the new zone's fields or the zone's other members.
 * The new zone reaches the draft only when the dialog calls commit().
 */
const zone = defineModel({ type: String, default: '' })
const props = defineProps({
  /** The select's id; the new zone's name field takes it with -new. */
  id: { type: String, required: true },
  /** The interface being edited, left out of the zone's other members. */
  iface: { type: String, default: '' },
  /** What a new zone is called until another name is typed. */
  suggested: { type: String, default: '' },
})

const config = useConfigStore()
const auth = useAuthStore()
const newName = ref(props.suggested)
const renamed = ref(false)
const newExternal = ref(false)
const error = ref('')

watch(
  () => props.suggested,
  (name) => {
    if (!renamed.value) newName.value = name
  },
)

const creating = computed(() => zone.value === NEW_ZONE)

/**
 * The interfaces in a zone with anti-lockout are where the router is
 * managed from, which is an admin's call: an operator moves none into such
 * a zone or out of one, as the server would refuse at apply.
 */
const guarded = (name) => config.zones.some((z) => z.name === name && z.antiLockout)
const current = computed(() => config.interfaces.find((i) => i.name === props.iface)?.zone ?? '')
const pinned = computed(() => auth.isOperator && guarded(current.value))
const barred = (name) => auth.isOperator && guarded(name) && name !== current.value
const hint = computed(() =>
  auth.isOperator && config.zones.some((z) => z.antiLockout)
    ? 'Firewall rules match on zones. Only an admin moves an interface into or out of one with anti-lockout.'
    : 'Firewall rules match on zones. Interfaces in the same zone share one rule list.',
)
const mates = computed(() =>
  config.interfaces
    .filter((i) => i.name !== props.iface && i.zone && i.zone === zone.value)
    .map((i) => i.name),
)

/**
 * Turns a pending new zone into a real one in the draft.
 * @returns {boolean} false when the name will not do, which it says
 */
function commit() {
  if (!creating.value) return true
  const name = newName.value.trim()
  if (!/^[a-z][a-z0-9_]{0,30}$/.test(name)) {
    error.value = 'Name must be lowercase letters, digits, or underscores and start with a letter.'
    return false
  }
  if (config.zones.some((z) => z.name === name)) {
    error.value = `Zone ${name} already exists. Pick it from the list.`
    return false
  }
  config.upsertZone(newExternal.value ? { name, external: true } : { name })
  zone.value = name
  error.value = ''
  return true
}

defineExpose({ commit })
</script>

<script>
/**
 * The "New zone…" choice while it is still pending. A real zone name is
 * [a-z][a-z0-9_]*, so the plus can never collide with one.
 */
export const NEW_ZONE = '+new'
</script>

<template>
  <FormField :id="id" label="Zone" :hint="hint">
    <select :id="id" v-model="zone" class="input" :disabled="pinned">
      <option value="">Unassigned (traffic dropped)</option>
      <option v-for="z in config.zones" :key="z.name" :value="z.name" :disabled="barred(z.name)">
        {{ z.name }}
      </option>
      <option :value="NEW_ZONE">New zone…</option>
    </select>
  </FormField>
  <div v-if="creating" class="space-y-3 sm:col-span-2">
    <FormField
      :id="`${id}-new`"
      label="New zone name"
      hint="Lowercase letters, digits, and underscores, like guest or iot."
    >
      <input
        :id="`${id}-new`"
        v-model="newName"
        class="input font-mono"
        autocapitalize="none"
        spellcheck="false"
        required
        @input="renamed = true"
      />
    </FormField>
    <ToggleRow
      v-model="newExternal"
      label="External"
      hint="IPv4 leaving it is masqueraded when outbound NAT is automatic."
    />
    <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
  </div>
  <p v-else-if="mates.length" class="text-sm text-ink-muted sm:col-span-2">
    Shares zone <span class="font-mono">{{ zone }}</span> with
    <span class="font-mono">{{ mates.join(', ') }}</span
    >, so they are all matched by the same rules.
  </p>
</template>
