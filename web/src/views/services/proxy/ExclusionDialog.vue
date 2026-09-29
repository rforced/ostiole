<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { sameExclusion } from '@/lib/exclusions'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The WAF profile the exclusion is in. */
  profile: { type: String, required: true },
  /** The exclusion being edited, or what a new one starts with. */
  exclusion: { type: Object, default: null },
  /** Its place in the profile's list; -1 adds it. */
  index: { type: Number, default: -1 },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return { rule: '', path: '', target: '', description: '' }
}

watch(
  () => [open.value, props.exclusion],
  () => {
    if (!open.value) return
    const e = props.exclusion ?? {}
    form.value = {
      rule: e.rule ?? '',
      path: e.path ?? '',
      target: e.target ?? '',
      description: e.description ?? '',
    }
  },
  { immediate: true },
)

const editing = computed(() => props.index >= 0)
const title = computed(() =>
  editing.value ? `Exclusion of rule ${props.exclusion?.rule}` : 'Add exclusion',
)

const entry = computed(() => ({
  rule: form.value.rule.trim(),
  path: form.value.path.trim(),
  target: form.value.target.trim(),
  description: form.value.description.trim(),
}))

/** What the router would refuse, said before the draft takes it. */
const problem = computed(() => {
  const { rule, path, target } = entry.value
  if (rule) {
    const m = /^([0-9]{1,9})(-([0-9]{1,9}))?$/.exec(rule)
    if (!m) return 'A rule is an ID or a range like 942100-942199.'
    if (Number(m[1]) < 1) return 'Rule IDs start at 1.'
    if (m[3] && Number(m[3]) <= Number(m[1])) return 'The range ends before it starts.'
  }
  if (path && !path.startsWith('/')) return 'A path starts with /.'
  if (/["\\]/.test(path)) return 'A path cannot hold a quote or a backslash.'
  if (/\p{Cc}/u.test(path)) return 'A path cannot hold a control character.'
  if (target && !/^[A-Z_]+(:[A-Za-z0-9_.[\]-]+)?$/.test(target)) {
    return 'A variable is written like ARGS or ARGS:password.'
  }
  const list = config.proxy.wafProfiles?.find((w) => w.id === props.profile)?.exclusions ?? []
  if (list.some((e, i) => i !== props.index && sameExclusion(e, entry.value))) {
    return 'That exclusion is in the profile already.'
  }
  return ''
})

function save() {
  const { rule, path, target, description } = entry.value
  const out = { rule }
  if (path) out.path = path
  if (target) out.target = target
  if (description) out.description = description
  if (editing.value) config.updateExclusion(props.profile, props.index, out)
  else config.addExclusion(props.profile, out)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="title" :description="`In WAF profile ${profile}.`">
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="exc-rule" label="Rule" hint="One ID, or a range like 942100-942199.">
        <input
          id="exc-rule"
          v-model="form.rule"
          class="input w-48 font-mono max-sm:w-full"
          required
          spellcheck="false"
          autocomplete="off"
        />
      </FormField>
      <FormField
        id="exc-path"
        label="Path"
        hint="Any path that starts with this. Empty means every path."
      >
        <input
          id="exc-path"
          v-model="form.path"
          class="input font-mono"
          spellcheck="false"
          autocomplete="off"
        />
      </FormField>
      <FormField
        id="exc-target"
        label="Variable"
        hint="One variable, like ARGS:password. Empty means every variable."
      >
        <input
          id="exc-target"
          v-model="form.target"
          class="input font-mono"
          spellcheck="false"
          autocomplete="off"
        />
      </FormField>
      <FormField id="exc-desc" label="Description">
        <input id="exc-desc" v-model="form.description" class="input" />
      </FormField>
      <p v-if="problem" role="alert" class="text-sm text-bad">{{ problem }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!entry.rule || Boolean(problem)">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
