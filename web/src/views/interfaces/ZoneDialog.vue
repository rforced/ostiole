<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import AppNotice from '@/components/AppNotice.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { nameProblem } from '@/lib/names'
import type { Zone } from '@/lib/types.gen'
import { ADMIN_ONLY, adminOnly, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  zone: { type: Object as PropType<Zone | null>, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const auth = useAuthStore()
const config = useConfigStore()
const form = ref<Zone>(blank())
// Anti-lockout is kept by zone name, so renaming such a zone changes it.
const nameLocked = computed(() => !auth.isAdmin && props.zone?.antiLockout === true)
/** Interfaces in this zone whose private-source block would run ahead of anti-lockout. */
const privateBlocked = computed(() => {
  const zone = props.zone?.name || form.value.name.trim()
  if (!zone) return []
  return config.interfaces.filter((i) => i.zone === zone && i.blockPrivate).map((i) => i.name)
})
const error = ref('')

function blank(): Zone {
  return { name: '', description: '', external: false, antiLockout: false, logDrops: false }
}

watch(
  () => [open.value, props.zone],
  () => {
    if (open.value) form.value = { ...blank(), ...(props.zone ?? {}) }
    error.value = ''
  },
  { immediate: true },
)

function save() {
  error.value = ''
  const name = form.value.name.trim()
  error.value = nameProblem('name', name)
  if (error.value) return
  const previous = props.zone?.name ?? name
  if (name !== props.zone?.name && config.zones.some((z) => z.name === name)) {
    error.value = `Zone ${name} already exists.`
    return
  }
  const out = { ...form.value, name }
  for (const k of ['description'] as const) if (!out[k]) delete out[k]
  for (const k of ['external', 'antiLockout', 'logDrops'] as const) if (!out[k]) delete out[k]
  config.upsertZone(out, previous)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="zone ? `Zone ${zone.name}` : 'Add zone'">
    <form id="zone-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField
          id="zone-name"
          label="Name"
          :hint="
            nameLocked && auth.isOperator ? adminOnly('rename', 'a zone with anti-lockout') : ''
          "
        >
          <input
            id="zone-name"
            v-model="form.name"
            :disabled="nameLocked"
            class="input font-mono"
            autocapitalize="none"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField id="zone-desc" label="Description">
          <input id="zone-desc" v-model="form.description" class="input" />
        </FormField>
      </div>
      <div class="space-y-2 text-sm">
        <ToggleRow
          v-model="form.external"
          label="External"
          hint="IPv4 leaving it is masqueraded when outbound NAT is automatic."
        />
        <ToggleRow
          v-model="form.antiLockout"
          label="Anti-lockout"
          :hint="
            auth.isOperator
              ? ADMIN_ONLY
              : 'The management ports are always reachable from this zone.'
          "
          :disabled="!auth.isAdmin"
        />
        <AppNotice v-if="form.antiLockout && privateBlocked.length">
          Blocking private sources is on for {{ privateBlocked.join(', ') }} and runs ahead of
          anti-lockout, so management from a private upstream will be dropped.
        </AppNotice>
        <ToggleRow
          v-model="form.logDrops"
          label="Log drops"
          hint="Every packet this router drops in this zone is logged: the ones no rule matched, and the ones it refused on its own."
        />
      </div>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="zone-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
