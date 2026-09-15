<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ alias: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref({ name: '', type: 'hosts', description: '', entries: '' })
const error = ref('')

watch(
  () => [open.value, props.alias],
  () => {
    if (!open.value) return
    error.value = ''
    const a = props.alias
    form.value = a
      ? {
          name: a.name,
          type: a.type,
          description: a.description ?? '',
          entries: joinList(a.entries),
        }
      : { name: '', type: 'hosts', description: '', entries: '' }
  },
  { immediate: true },
)

function save() {
  error.value = ''
  const name = form.value.name.trim()
  if (!/^[a-z][a-z0-9_]{0,30}$/.test(name)) {
    error.value = 'Name must be lowercase letters, digits, or underscores and start with a letter.'
    return
  }
  const previous = props.alias?.name ?? name
  if (name !== previous && config.aliases.some((a) => a.name === name)) {
    error.value = `Alias ${name} already exists.`
    return
  }
  const out = { name, type: form.value.type, entries: parseList(form.value.entries) }
  if (form.value.description) out.description = form.value.description
  config.upsertAlias(out, previous)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="alias ? `Alias ${alias.name}` : 'New alias'"
    description="A named list you can reuse in rules. Host aliases become nftables sets."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="alias-name" label="Name">
          <input
            id="alias-name"
            v-model="form.name"
            class="input font-mono"
            autocapitalize="none"
            spellcheck="false"
            required
          />
        </FormField>
        <FormField id="alias-type" label="Type">
          <select id="alias-type" v-model="form.type" class="input">
            <option value="hosts">Hosts and networks</option>
            <option value="ports">Ports</option>
          </select>
        </FormField>
      </div>
      <FormField id="alias-desc" label="Description">
        <input id="alias-desc" v-model="form.description" class="input" />
      </FormField>
      <FormField
        id="alias-entries"
        label="Entries"
        :hint="
          form.type === 'hosts'
            ? 'One per line: IPs or CIDR networks.'
            : 'One per line: ports or ranges like 8000-8100.'
        "
      >
        <textarea
          id="alias-entries"
          v-model="form.entries"
          class="input h-32 font-mono"
          spellcheck="false"
        ></textarea>
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
