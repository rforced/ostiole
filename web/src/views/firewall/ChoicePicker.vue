<script setup>
import { computed, ref } from 'vue'

import SearchBox from '@/components/SearchBox.vue'
import { conditionKey, formatCondition } from '@/lib/conditions'
import { matches } from '@/lib/search'

/** The conditions the alias keeps part of its list by, as written. */
const selected = defineModel({ type: Array, default: () => [] })

const props = defineProps({
  /**
   * What the list can be narrowed by: a field and the values it holds, or,
   * with no field, the keys its addresses are listed under.
   *
   * @type {import('vue').PropType<{ field?: string, values: string[] }[]>}
   */
  choices: { type: Array, default: () => [] },
})

const filter = ref('')
const chosen = computed(() => new Set(selected.value.map(conditionKey)))

/** Every condition the list offers, in its order. */
const offered = computed(() =>
  props.choices.flatMap((c) => c.values.map((v) => formatCondition(c.field ?? '', v))),
)

/** The groups with the values the filter leaves, each with its condition. */
const groups = computed(() =>
  props.choices
    .map((c) => {
      const field = c.field ?? ''
      const all = matches(filter.value, [field])
      return {
        key: field || '(keys)',
        title: field || 'Listed under',
        items: c.values
          .filter((v) => all || matches(filter.value, [v]))
          .map((v) => ({ value: v, condition: formatCondition(field, v) })),
      }
    })
    .filter((g) => g.items.length),
)

/**
 * Conditions the list does not offer: a pattern, or a value it no longer
 * holds. They are shown so they can be removed, and never quietly dropped.
 */
const others = computed(() => {
  const known = new Set(offered.value.map(conditionKey))
  return selected.value.filter((c) => !known.has(conditionKey(c)))
})

function isChosen(condition) {
  return chosen.value.has(conditionKey(condition))
}

function set(conditions, on) {
  const next = new Set(chosen.value)
  for (const c of conditions) {
    if (on) next.add(conditionKey(c))
    else next.delete(conditionKey(c))
  }
  // The list's order, then the rest as they were, so the diff of a saved
  // alias stays readable.
  selected.value = offered.value
    .filter((c) => next.has(conditionKey(c)))
    .concat(others.value.filter((c) => next.has(conditionKey(c))))
}
</script>

<template>
  <div class="space-y-2">
    <div class="flex flex-wrap items-center gap-2">
      <SearchBox v-model="filter" placeholder="value or field" />
      <button v-if="selected.length" type="button" class="btn-secondary" @click="selected = []">
        Clear {{ selected.length }}
      </button>
    </div>

    <p class="text-sm text-ink-muted">
      <template v-if="selected.length">
        {{ selected.length }} ticked:
        <span class="font-mono text-ink-2">
          {{ selected.slice(0, 8).join(', ')
          }}<span v-if="selected.length > 8" class="font-sans">
            and {{ selected.length - 8 }} more</span
          >
        </span>
      </template>
      <template v-else>Nothing ticked.</template>
    </p>

    <div class="max-h-72 overflow-y-auto rounded-lg border border-line p-2">
      <p v-if="!groups.length" class="p-2 text-sm text-ink-muted">
        Nothing matches "{{ filter.trim() }}".
      </p>
      <div v-for="g in groups" :key="g.key" class="mb-2 last:mb-0">
        <div class="flex items-baseline gap-2 px-1 py-1">
          <h3 class="group-title">{{ g.title }}</h3>
          <button
            type="button"
            class="link"
            @click="
              set(
                g.items.map((i) => i.condition),
                !g.items.every((i) => isChosen(i.condition)),
              )
            "
          >
            {{ g.items.every((i) => isChosen(i.condition)) ? 'none' : 'all' }}
          </button>
        </div>
        <div class="grid grid-cols-2 gap-x-4 sm:grid-cols-3">
          <label
            v-for="i in g.items"
            :key="i.condition"
            class="flex items-center gap-2 rounded px-1 py-0.5 text-sm hover:bg-surface-2"
          >
            <input
              type="checkbox"
              class="size-4 rounded border-line-2"
              :checked="isChosen(i.condition)"
              @change="set([i.condition], $event.target.checked)"
            />
            <span class="truncate font-mono text-code" :title="i.value">{{ i.value }}</span>
          </label>
        </div>
      </div>
    </div>

    <div v-if="others.length" class="flex flex-wrap items-baseline gap-x-3 text-sm text-ink-muted">
      <span>Also kept, and not in this list:</span>
      <span v-for="c in others" :key="c" class="inline-flex items-baseline gap-1">
        <span class="font-mono">{{ c }}</span>
        <button
          type="button"
          class="link"
          :aria-label="`Stop keeping ${c}`"
          @click="set([c], false)"
        >
          remove
        </button>
      </span>
    </div>
  </div>
</template>
