<script setup>
import { Search } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const blocking = computed(() => config.ensureBlocking())

/** One name per line, kept out of the model when empty. */
function listField(key) {
  return computed({
    get: () => joinList(blocking.value[key] ?? []),
    set: (v) => {
      const list = parseList(v)
      if (list.length) blocking.value[key] = list
      else delete blocking.value[key]
    },
  })
}

const allow = listField('allow')
const deny = listField('deny')

const query = ref('')
const finding = ref(null)

/** Asks the router what it would do with a name, and why. */
const lookup = useAsync(async () => {
  const name = query.value.trim()
  if (!name) return
  finding.value = null
  finding.value = await api.blocking.lookup(name)
})

/** The sentence the lookup result comes to. */
const verdict = computed(() => {
  const f = finding.value
  if (!f) return ''
  switch (f.reason) {
    case 'not-a-name':
      return 'That is not a domain name.'
    case 'allow':
      return `Not blocked: the allow list has ${f.matched}.`
    case 'never':
      return `Not blocked: this router answers for ${f.matched} itself.`
    case 'delegated':
      return `Not blocked: ${f.matched} is a domain override, answered by its own resolvers.`
    case 'off':
      return 'Not blocked: DNS blocking is off.'
    case 'deny':
      return `Blocked because the deny list has ${f.matched}.`
    case 'canary':
      return 'Blocked: Firefox asks this name before turning on DNS over HTTPS.'
    case 'list':
      return f.matched === f.name
        ? 'Blocked: a list has it.'
        : `Blocked: a list has ${f.matched}, which covers it.`
    default:
      return 'Not blocked: no list has it.'
  }
})

/** The lookup reflects the router, not the unapplied draft. */
const stale = computed(() => config.dirty)
</script>

<template>
  <div class="space-y-6">
    <div class="grid max-w-4xl gap-4 sm:grid-cols-2">
      <FormField
        id="block-allow"
        label="Never block"
        hint="One name per line. A name here covers everything under it, and beats every list."
      >
        <textarea
          id="block-allow"
          v-model="allow"
          rows="8"
          class="input font-mono"
          spellcheck="false"
          placeholder="analytics.example.com"
        ></textarea>
      </FormField>
      <FormField
        id="block-deny"
        label="Always block"
        hint="One name per line, subdomains included, whether a list names it or not."
      >
        <textarea
          id="block-deny"
          v-model="deny"
          rows="8"
          class="input font-mono"
          spellcheck="false"
          placeholder="ads.example.net"
        ></textarea>
      </FormField>
    </div>

    <p class="max-w-3xl text-sm text-neutral-500">
      Names this router answers for itself are never blocked, whatever a list says.
    </p>

    <section class="max-w-3xl space-y-3" aria-labelledby="lookup-title">
      <h2 id="lookup-title" class="section-title">Is a name blocked?</h2>
      <form class="flex flex-wrap items-end gap-2" @submit.prevent="lookup.run">
        <FormField
          id="lookup-name"
          label="Name"
          hint="Answers from what is applied, and says which list decided it."
          class="flex-1"
        >
          <input
            id="lookup-name"
            v-model="query"
            class="input font-mono"
            spellcheck="false"
            placeholder="ads.doubleclick.net"
          />
        </FormField>
        <button type="submit" class="btn-secondary mb-1" :disabled="lookup.busy.value">
          <Search class="mr-1 size-4" aria-hidden="true" /> Look up
        </button>
      </form>

      <p v-if="lookup.error.value" role="alert" class="text-sm text-red-600 dark:text-red-400">
        {{ lookup.error.value }}
      </p>

      <div
        v-if="finding"
        class="rounded-lg border border-neutral-200 p-3 text-sm dark:border-neutral-800"
      >
        <p>
          <span class="font-mono">{{ finding.name }}</span>
          <span class="badge ml-2" :class="finding.blocked ? 'badge-warn' : 'badge-ok'">{{
            finding.blocked ? 'blocked' : 'not blocked'
          }}</span>
        </p>
        <p class="mt-1 text-neutral-600 dark:text-neutral-400">{{ verdict }}</p>
        <p v-if="finding.lists?.length" class="mt-1 text-neutral-500">
          On: <span class="font-mono">{{ finding.lists.join(', ') }}</span>
        </p>
        <p v-if="stale" class="mt-2 text-sm text-neutral-500">
          This answer ignores the draft's unapplied changes.
        </p>
      </div>
    </section>
  </div>
</template>
