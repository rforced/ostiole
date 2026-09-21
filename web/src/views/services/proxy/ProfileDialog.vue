<script setup>
import { Plus, X } from 'lucide-vue-next'
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The profile being edited, or null for a new one. */
  profile: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

/** The application exclusion sets the sidecar carries. */
const APPLICATIONS = [
  'wordpress',
  'nextcloud',
  'drupal',
  'phpbb',
  'phpmyadmin',
  'dokuwiki',
  'xenforo',
  'cpanel',
]

function blank() {
  return {
    id: '',
    previousId: '',
    description: '',
    mode: 'detect',
    paranoia: 1,
    inboundThreshold: 0,
    outboundThreshold: 0,
    applications: [],
    bodyLimitMB: 0,
    inspectResponses: false,
    exclusions: [],
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
      inboundThreshold: w.inboundThreshold ?? 0,
      outboundThreshold: w.outboundThreshold ?? 0,
      applications: [...(w.applications ?? [])],
      bodyLimitMB: w.bodyLimitMB ?? 0,
      inspectResponses: Boolean(w.inspectResponses),
      // The events tab writes a rule and a note and nothing else; the
      // form has every field.
      exclusions: (w.exclusions ?? []).map((e) => ({
        rule: e.rule ?? '',
        path: e.path ?? '',
        target: e.target ?? '',
        description: e.description ?? '',
      })),
    }
  },
  { immediate: true },
)

function toggleApp(name, on) {
  const list = new Set(form.value.applications)
  if (on) list.add(name)
  else list.delete(name)
  form.value.applications = APPLICATIONS.filter((a) => list.has(a))
}

function addExclusion() {
  form.value.exclusions.push({ rule: '', path: '', target: '', description: '' })
}

function removeExclusion(index) {
  form.value.exclusions.splice(index, 1)
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
  if (f.inspectResponses) profile.inspectResponses = true
  const exclusions = f.exclusions
    .filter((e) => e.rule.trim())
    .map((e) => {
      const out = { rule: e.rule.trim() }
      if (e.path.trim()) out.path = e.path.trim()
      if (e.target.trim()) out.target = e.target.trim()
      if (e.description.trim()) out.description = e.description.trim()
      return out
    })
  if (exclusions.length) profile.exclusions = exclusions
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
      <div class="grid gap-4 sm:grid-cols-2">
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
        <p class="text-xs text-ink-muted">Detect only records what would have been blocked.</p>
      </fieldset>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          id="waf-paranoia"
          label="Paranoia"
          hint="1. Higher levels block more of ordinary traffic."
        >
          <select id="waf-paranoia" v-model.number="form.paranoia" class="input w-24">
            <option v-for="n in 4" :key="n" :value="n">{{ n }}</option>
          </select>
        </FormField>
        <FormField
          id="waf-body"
          label="Inspect request body up to"
          hint="12 MB. The rest passes uninspected."
        >
          <input
            id="waf-body"
            v-model.number="form.bodyLimitMB"
            type="number"
            min="0"
            max="1024"
            class="input w-32 font-mono"
          />
        </FormField>
        <FormField id="waf-inbound" label="Request threshold" hint="5.">
          <input
            id="waf-inbound"
            v-model.number="form.inboundThreshold"
            type="number"
            min="0"
            max="1000"
            class="input w-32 font-mono"
          />
        </FormField>
        <FormField id="waf-outbound" label="Response threshold" hint="4.">
          <input
            id="waf-outbound"
            v-model.number="form.outboundThreshold"
            type="number"
            min="0"
            max="1000"
            class="input w-32 font-mono"
          />
        </FormField>
      </div>

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

      <fieldset class="space-y-2">
        <legend class="group-title">Exclusions</legend>
        <div
          v-for="(e, i) in form.exclusions"
          :key="i"
          class="grid grid-cols-[6rem_1fr_1fr_1fr_auto] items-center gap-2"
        >
          <input
            v-model="e.rule"
            class="input font-mono"
            placeholder="942100"
            spellcheck="false"
            :aria-label="`Rule ${i + 1}`"
          />
          <input
            v-model="e.path"
            class="input font-mono"
            placeholder="/wp-admin"
            spellcheck="false"
            :aria-label="`Path for rule ${i + 1}`"
          />
          <input
            v-model="e.target"
            class="input font-mono"
            placeholder="ARGS:content"
            spellcheck="false"
            :aria-label="`Variable for rule ${i + 1}`"
          />
          <input v-model="e.description" class="input" :aria-label="`Note for rule ${i + 1}`" />
          <button
            type="button"
            class="icon-btn"
            :aria-label="`Remove exclusion ${i + 1}`"
            @click="removeExclusion(i)"
          >
            <X class="size-4" />
          </button>
        </div>
        <button type="button" class="btn-secondary" @click="addExclusion">
          <Plus class="size-4" aria-hidden="true" /> Add exclusion
        </button>
      </fieldset>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
