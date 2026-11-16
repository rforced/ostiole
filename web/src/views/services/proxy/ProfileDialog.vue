<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import type { WAFProfile } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The profile being edited, or null for a new one. */
  profile: { type: Object as PropType<WAFProfile | null>, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

/** The application exclusion sets the sidecar carries. */
const APPLICATIONS = [
  'wordpress',
  'nextcloud',
  'phpbb',
  'xenforo',
  'vaultwarden',
  'jellyfin',
  'tendrils',
  'cavu',
]

/** An empty number is the default, which its placeholder shows. */
interface ProfileForm {
  id: string
  previousId: string
  description: string
  mode: string
  paranoia: number
  inboundThreshold: number | ''
  outboundThreshold: number | ''
  applications: string[]
  bodyLimitMB: number | ''
  passLargeBodies: boolean
  inspectResponses: boolean
  maxCookies: number | ''
  maxParts: number | ''
  maxNameDepth: number | ''
  maxNameLength: number | ''
  refuseMultipart: boolean
}

function blank(): ProfileForm {
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
    maxCookies: '',
    maxParts: '',
    maxNameDepth: '',
    maxNameLength: '',
    refuseMultipart: false,
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
      maxCookies: w.maxCookies || '',
      maxParts: w.maxParts || '',
      maxNameDepth: w.maxNameDepth || '',
      maxNameLength: w.maxNameLength || '',
      refuseMultipart: Boolean(w.refuseMultipart),
    }
  },
  { immediate: true },
)

const bodyHint = computed(() =>
  form.value.passLargeBodies
    ? '12 MB is the default. Past it, a body goes through with the rest unread.'
    : '12 MB is the default. A larger one is refused, an upload too.',
)

function toggleApp(name: string, on: boolean) {
  const list = new Set(form.value.applications)
  if (on) list.add(name)
  else list.delete(name)
  form.value.applications = APPLICATIONS.filter((a) => list.has(a))
}

function save() {
  const f = form.value
  const profile: WAFProfile = { id: f.id.trim() }
  if (f.description.trim()) profile.description = f.description.trim()
  if (f.mode === 'block') profile.mode = 'block'
  else if (props.profile?.mode === 'detect') profile.mode = 'detect'
  const was = props.profile?.paranoia
  if (Number(f.paranoia) !== (was || 1)) profile.paranoia = Number(f.paranoia)
  else if (was != null) profile.paranoia = was
  if (Number(f.inboundThreshold)) profile.inboundThreshold = Number(f.inboundThreshold)
  if (Number(f.outboundThreshold)) profile.outboundThreshold = Number(f.outboundThreshold)
  if (f.applications.length) profile.applications = [...f.applications]
  if (Number(f.bodyLimitMB)) profile.bodyLimitMB = Number(f.bodyLimitMB)
  if (f.passLargeBodies) profile.passLargeBodies = true
  if (f.inspectResponses) profile.inspectResponses = true
  if (Number(f.maxCookies)) profile.maxCookies = Number(f.maxCookies)
  if (Number(f.maxParts)) profile.maxParts = Number(f.maxParts)
  if (Number(f.maxNameDepth)) profile.maxNameDepth = Number(f.maxNameDepth)
  if (Number(f.maxNameLength)) profile.maxNameLength = Number(f.maxNameLength)
  if (f.refuseMultipart) profile.refuseMultipart = true
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
    <form id="profile-form" class="space-y-4" @submit.prevent="save">
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

      <fieldset class="field-group">
        <legend>Mode</legend>
        <ToggleRow v-model="form.mode" variant="radio" value="detect" label="Detect only" />
        <ToggleRow v-model="form.mode" variant="radio" value="block" label="Block" />
        <p class="text-sm text-ink-muted">Detect only records what would have been blocked.</p>
      </fieldset>

      <div class="fields">
        <FormField
          id="waf-paranoia"
          label="Paranoia"
          hint="1 is the default. Higher levels block more of ordinary traffic."
        >
          <select id="waf-paranoia" v-model.number="form.paranoia" class="input w-32 max-sm:w-full">
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

      <fieldset class="field-group">
        <legend>Request limits</legend>
        <div class="fields">
          <FormField
            id="waf-cookies"
            label="Cookies per request"
            hint="200 is the default. A request with more is blocked."
          >
            <input
              id="waf-cookies"
              v-model.number="form.maxCookies"
              type="number"
              min="0"
              max="10000"
              placeholder="200"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
          <FormField
            id="waf-parts"
            label="Multipart parts"
            hint="200 is the default. A body with more fields and files together is blocked."
          >
            <input
              id="waf-parts"
              v-model.number="form.maxParts"
              type="number"
              min="0"
              max="10000"
              placeholder="200"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
          <FormField
            id="waf-depth"
            label="Brackets per argument name"
            hint="16 is the default. A name with more [ in it is blocked."
          >
            <input
              id="waf-depth"
              v-model.number="form.maxNameDepth"
              type="number"
              min="0"
              max="64"
              placeholder="16"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
          <FormField
            id="waf-name-length"
            label="Characters per argument name"
            hint="256 is the default. A longer argument or cookie name is blocked."
          >
            <input
              id="waf-name-length"
              v-model.number="form.maxNameLength"
              type="number"
              min="16"
              max="8192"
              placeholder="256"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
        </div>
        <ToggleRow
          id="waf-refuse-multipart"
          v-model="form.refuseMultipart"
          label="Refuse multipart bodies"
          hint="For a site that takes no uploads. Excluding rule 19008 lets them through on an upload path."
        />
      </fieldset>

      <fieldset class="field-group">
        <legend>Applications</legend>
        <div class="flex flex-wrap gap-4">
          <label v-for="a in APPLICATIONS" :key="a" class="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              class="checkbox"
              :checked="form.applications.includes(a)"
              @change="toggleApp(a, ($event.target as HTMLInputElement).checked)"
            />
            {{ a }}
          </label>
        </div>
      </fieldset>

      <ToggleRow v-model="form.inspectResponses" label="Inspect responses" />
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="profile-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
