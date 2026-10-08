<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The profile being edited, or null for a new one. */
  profile: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

/** The application exclusion sets the sidecar carries. */
const APPLICATIONS = ['wordpress', 'nextcloud', 'phpbb', 'xenforo', 'vaultwarden', 'jellyfin']

/** An empty number is the default, which its placeholder shows. */
function blank() {
  return {
    id: '',
    previousId: '',
    description: '',
    mode: 'detect',
    paranoia: 1,
    inboundThreshold: '',
    outboundThreshold: '',
    applications: [],
    bodyLimitMB: '',
    passLargeBodies: false,
    inspectResponses: false,
  }
}

watch(
  () => [open.value, props.profile],
  () => {
    if (!open.value) return
    const w = props.profile
    if (!w) {
      form.value = blank()
      return
    }
    form.value = {
      ...blank(),
      id: w.id,
      previousId: w.id,
      description: w.description ?? '',
      mode: w.mode === 'block' ? 'block' : 'detect',
      paranoia: w.paranoia || 1,
      inboundThreshold: w.inboundThreshold || '',
      outboundThreshold: w.outboundThreshold || '',
      applications: [...(w.applications ?? [])],
      bodyLimitMB: w.bodyLimitMB || '',
      passLargeBodies: Boolean(w.passLargeBodies),
      inspectResponses: Boolean(w.inspectResponses),
    }
  },
  { immediate: true },
)

const bodyHint = computed(() =>
  form.value.passLargeBodies
    ? '12 MB is the default. Past it, a body goes through with the rest unread.'
    : '12 MB is the default. A larger one is refused, an upload too.',
)

function toggleApp(name, on) {
  const list = new Set(form.value.applications)
  if (on) list.add(name)
  else list.delete(name)
  form.value.applications = APPLICATIONS.filter((a) => list.has(a))
}

function save() {
  const f = form.value
  const profile = { id: f.id.trim() }
  if (f.description.trim()) profile.description = f.description.trim()
  if (f.mode === 'block') profile.mode = 'block'
  if (Number(f.paranoia) !== 1) profile.paranoia = Number(f.paranoia)
  if (Number(f.inboundThreshold)) profile.inboundThreshold = Number(f.inboundThreshold)
  if (Number(f.outboundThreshold)) profile.outboundThreshold = Number(f.outboundThreshold)
  if (f.applications.length) profile.applications = [...f.applications]
  if (Number(f.bodyLimitMB)) profile.bodyLimitMB = Number(f.bodyLimitMB)
  if (f.passLargeBodies) profile.passLargeBodies = true
  if (f.inspectResponses) profile.inspectResponses = true
  // The exclusions card edits those; a rename takes them along.
  const exclusions = config.proxy.wafProfiles?.find((w) => w.id === f.previousId)?.exclusions
  if (exclusions?.length) profile.exclusions = exclusions
  config.upsertProfile(profile, f.previousId || profile.id)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="profile ? `Profile ${profile.id}` : 'Add WAF profile'"
    description="How requests to a site are inspected."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="waf-id" label="Name">
          <input
            id="waf-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="waf-desc" label="Description">
          <input id="waf-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <fieldset class="space-y-2">
        <legend class="group-title">Mode</legend>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.mode" type="radio" value="detect" class="size-4" />
          Detect only
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.mode" type="radio" value="block" class="size-4" />
          Block
        </label>
        <p class="text-sm text-ink-muted">Detect only records what would have been blocked.</p>
      </fieldset>

      <div class="fields">
        <FormField
          id="waf-paranoia"
          label="Paranoia"
          hint="1 is the default. Higher levels block more of ordinary traffic."
        >
          <select id="waf-paranoia" v-model.number="form.paranoia" class="input w-24 max-sm:w-full">
            <option v-for="n in 4" :key="n" :value="n">{{ n }}</option>
          </select>
        </FormField>
        <FormField id="waf-body" label="Request body limit" :hint="bodyHint">
          <input
            id="waf-body"
            v-model.number="form.bodyLimitMB"
            type="number"
            min="1"
            max="1024"
            placeholder="12"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
        <FormField
          id="waf-inbound"
          label="Request threshold"
          hint="5 is the default. A request is blocked once the rules it matches score this much. A critical rule scores 5."
        >
          <input
            id="waf-inbound"
            v-model.number="form.inboundThreshold"
            type="number"
            min="1"
            max="1000"
            placeholder="5"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
        <FormField
          id="waf-outbound"
          label="Response threshold"
          hint="4 is the default. A response is blocked once the rules it matches score this much."
        >
          <input
            id="waf-outbound"
            v-model.number="form.outboundThreshold"
            type="number"
            min="1"
            max="1000"
            placeholder="4"
            class="input w-32 font-mono max-sm:w-full"
          />
        </FormField>
      </div>

      <ToggleRow
        v-model="form.passLargeBodies"
        label="Let larger bodies through"
        hint="Only the part up to the limit is inspected, and the rest reaches the site unread. For a site that takes larger uploads."
      />

      <fieldset class="space-y-2">
        <legend class="group-title">Applications</legend>
        <div class="flex flex-wrap gap-4">
          <label v-for="a in APPLICATIONS" :key="a" class="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              class="size-4 rounded border-line-2"
              :checked="form.applications.includes(a)"
              @change="toggleApp(a, $event.target.checked)"
            />
            {{ a }}
          </label>
        </div>
      </fieldset>

      <label class="flex items-center gap-2 text-sm">
        <input
          v-model="form.inspectResponses"
          type="checkbox"
          class="size-4 rounded border-line-2"
        />
        Inspect responses
      </label>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
