<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import SearchBox from '@/components/SearchBox.vue'
import { useAsync } from '@/lib/async'
import { formatCount, formatWhen } from '@/lib/format'

/** Entries on a page. */
const PAGE = 1000
/** How long typing rests before the router is asked. */
const SETTLE_MS = 250

/**
 * What a list the router keeps holds, a page at a time: the addresses an
 * alias fetched, the names on a blocklist. The router searches and pages,
 * because a list can run to a million entries.
 */
const props = defineProps({
  title: { type: String, required: true },
  /** What the search matches, e.g. "address or network". */
  placeholder: { type: String, required: true },
  /** The list's word for what it holds, e.g. Entries. */
  label: { type: String, required: true },
  /**
   * Reads one page: (q, offset, limit) resolving to
   * {total, matches, offset, items, fetchedAt}.
   */
  read: { type: Function, required: true },
})
const open = defineModel('open', { type: Boolean, default: false })

const query = ref('')
const page = ref(null)
/** Which way the list is being paged, for its button's spinner. */
const going = ref('')
const list = ref(null)

const load = useAsync(async (offset) => {
  page.value = await props.read(query.value.trim(), offset, PAGE)
})

// A new page shows from its top, not where the last one was scrolled to.
watch(
  page,
  () => {
    if (list.value) list.value.scrollTop = 0
  },
  { flush: 'post' },
)

let settle = 0
watch(query, () => {
  window.clearTimeout(settle)
  if (open.value) settle = window.setTimeout(() => load.run(0), SETTLE_MS)
})
watch(
  open,
  (on) => {
    window.clearTimeout(settle)
    if (!on) {
      query.value = ''
      return
    }
    page.value = null
    load.run(0)
  },
  { immediate: true },
)
onBeforeUnmount(() => window.clearTimeout(settle))

const items = computed(() => page.value?.items ?? [])
const at = computed(() => page.value?.offset ?? 0)
const hasPrevious = computed(() => at.value > 0)
const hasNext = computed(() => at.value + items.value.length < (page.value?.matches ?? 0))

async function turn(direction) {
  going.value = direction
  await load.run(direction === 'next' ? at.value + PAGE : Math.max(0, at.value - PAGE))
  going.value = ''
}

const empty = computed(() => {
  if (!page.value) return 'Reading…'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return 'Nothing fetched yet.'
})
</script>

<template>
  <AppDialog v-model:open="open" :title="title" :read-only="false">
    <div class="space-y-4">
      <SearchBox
        v-model="query"
        :placeholder="placeholder"
        :shown="page?.matches ?? 0"
        :total="page ? page.total : null"
      />
      <ErrorLine v-if="load.error.value" class="text-sm">{{ load.error.value }}</ErrorLine>
      <ul
        v-if="items.length"
        ref="list"
        :aria-label="label"
        class="max-h-[50dvh] overflow-y-auto rounded-md border border-line px-3 py-2 font-mono text-code break-all"
      >
        <li v-for="e in items" :key="e">{{ e }}</li>
      </ul>
      <p v-else-if="!load.error.value" class="text-ink-muted">{{ empty }}</p>
      <div class="flex flex-wrap items-center gap-3">
        <button
          v-if="hasPrevious"
          type="button"
          class="btn-secondary"
          :disabled="load.busy.value"
          :aria-busy="going === 'previous'"
          @click="turn('previous')"
        >
          <LoaderCircle
            v-if="going === 'previous'"
            class="size-4 animate-spin"
            aria-hidden="true"
          />
          Previous
        </button>
        <button
          v-if="hasNext"
          type="button"
          class="btn-secondary"
          :disabled="load.busy.value"
          :aria-busy="going === 'next'"
          @click="turn('next')"
        >
          <LoaderCircle v-if="going === 'next'" class="size-4 animate-spin" aria-hidden="true" />
          Next
        </button>
        <span v-if="items.length" class="text-sm text-ink-muted">
          {{ formatCount(at + 1) }}–{{ formatCount(at + items.length) }} of
          {{ formatCount(page.matches) }}.
          <template v-if="page.fetchedAt"> Fetched {{ formatWhen(page.fetchedAt) }}. </template>
        </span>
      </div>
    </div>
  </AppDialog>
</template>
