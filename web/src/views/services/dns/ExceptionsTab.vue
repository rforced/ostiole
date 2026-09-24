<script setup>
import { LoaderCircle, Search } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { sentence } from '@/lib/blocking'
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
const verdict = computed(() => sentence(finding.value))

/** The lookup reflects the router, not the unapplied draft. */
const stale = computed(() => config.dirty)
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Exceptions"
      intro="Names this router answers for itself are never blocked, whatever a list says."
    >
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
          hint="One name per line, subdomains included, whether the lists are on or not."
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
    </SectionCard>

    <SectionCard
      title="Is a name blocked?"
      intro="Answers from what is applied, and says which list decided it."
    >
      <div class="max-w-3xl space-y-3">
        <form class="form-row" @submit.prevent="lookup.run">
          <FormField id="lookup-name" label="Name" class="flex-1">
            <input
              id="lookup-name"
              v-model="query"
              class="input font-mono"
              spellcheck="false"
              placeholder="ads.doubleclick.net"
            />
          </FormField>
          <button
            type="submit"
            class="btn-secondary"
            :disabled="lookup.busy.value"
            :aria-busy="lookup.busy.value"
          >
            <LoaderCircle v-if="lookup.busy.value" class="size-4 animate-spin" aria-hidden="true" />
            <Search v-else class="size-4" aria-hidden="true" />
            Look up
          </button>
        </form>

        <p v-if="lookup.error.value" role="alert" class="text-bad">
          {{ lookup.error.value }}
        </p>

        <div v-if="finding" class="rounded-lg border border-line bg-page p-3">
          <p>
            <span class="font-mono">{{ finding.name }}</span>
            <span class="badge ml-2" :class="finding.blocked ? 'badge-warn' : 'badge-ok'">{{
              finding.blocked ? 'blocked' : 'not blocked'
            }}</span>
          </p>
          <p class="mt-1 text-ink-2">{{ verdict }}</p>
          <p v-if="finding.lists?.length" class="mt-1 text-ink-muted">
            On: <span class="font-mono">{{ finding.lists.join(', ') }}</span>
          </p>
          <p v-if="stale" class="mt-2 text-ink-muted">
            This answer ignores the draft's unapplied changes.
          </p>
        </div>
      </div>
    </SectionCard>
  </div>
</template>
