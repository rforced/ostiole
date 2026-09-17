<script setup>
import { computed, ref } from 'vue'

import { COUNTRIES, PRESETS, REGIONS } from '@/lib/countries'

/** The two-letter codes the alias holds. */
const selected = defineModel({ type: Array, default: () => [] })

const filter = ref('')
const chosen = computed(() => new Set(selected.value))

const matches = computed(() => {
  const q = filter.value.trim().toLowerCase()
  if (!q) return COUNTRIES
  return COUNTRIES.filter((c) => c.name.toLowerCase().includes(q) || c.code.includes(q))
})

/** Only the regions the filter left something in. */
const groups = computed(() =>
  REGIONS.map((region) => ({
    region,
    countries: matches.value.filter((c) => c.region === region),
  })).filter((g) => g.countries.length),
)

/**
 * Codes that are selected but not in the list: a code typed by hand, or one
 * the tz database has since dropped. They are shown so they can be removed,
 * and never quietly discarded.
 */
const unknown = computed(() => {
  const known = new Set(COUNTRIES.map((c) => c.code))
  return selected.value.filter((c) => !known.has(c))
})

const names = computed(() => Object.fromEntries(COUNTRIES.map((c) => [c.code, c.name])))

function set(codes, on) {
  const next = new Set(selected.value)
  for (const code of codes) {
    if (on) next.add(code)
    else next.delete(code)
  }
  // Keep the order stable so the diff of a saved alias stays readable.
  selected.value = COUNTRIES.map((c) => c.code)
    .filter((c) => next.has(c))
    .concat(unknown.value.filter((c) => next.has(c)))
}

const allShown = computed(() => matches.value.every((c) => chosen.value.has(c.code)))
</script>

<template>
  <div class="space-y-2">
    <div class="flex flex-wrap items-center gap-2">
      <input
        v-model="filter"
        type="search"
        class="input w-48"
        placeholder="Find a country"
        aria-label="Find a country"
      />
      <button
        v-for="p in PRESETS"
        :key="p.name"
        type="button"
        class="btn-secondary"
        @click="set(p.codes, true)"
      >
        + {{ p.name }}
      </button>
      <button
        v-if="matches.length && !allShown"
        type="button"
        class="btn-secondary"
        @click="
          set(
            matches.map((c) => c.code),
            true,
          )
        "
      >
        + All {{ filter ? 'matching' : 'countries' }}
      </button>
      <button v-if="selected.length" type="button" class="btn-secondary" @click="selected = []">
        Clear {{ selected.length }}
      </button>
    </div>

    <p class="text-sm text-neutral-500">
      <template v-if="selected.length">
        {{ selected.length }} selected:
        <span class="text-neutral-700 dark:text-neutral-300">
          {{
            selected
              .slice(0, 8)
              .map((c) => names[c] ?? c)
              .join(', ')
          }}<span v-if="selected.length > 8"> and {{ selected.length - 8 }} more</span>
        </span>
      </template>
      <template v-else>No countries yet.</template>
    </p>

    <div
      class="max-h-72 overflow-y-auto rounded-lg border border-neutral-200 p-2 dark:border-neutral-800"
    >
      <p v-if="!groups.length" class="p-2 text-sm text-neutral-500">
        Nothing matches "{{ filter }}".
      </p>
      <div v-for="g in groups" :key="g.region" class="mb-2 last:mb-0">
        <div class="flex items-baseline gap-2 px-1 py-1">
          <h3 class="text-xs font-medium tracking-wide text-neutral-500 uppercase">
            {{ g.region }}
          </h3>
          <button
            type="button"
            class="link"
            @click="
              set(
                g.countries.map((c) => c.code),
                !g.countries.every((c) => chosen.has(c.code)),
              )
            "
          >
            {{ g.countries.every((c) => chosen.has(c.code)) ? 'none' : 'all' }}
          </button>
        </div>
        <div class="grid grid-cols-2 gap-x-4 sm:grid-cols-3">
          <label
            v-for="c in g.countries"
            :key="c.code"
            class="flex items-center gap-2 rounded px-1 py-0.5 text-sm hover:bg-neutral-100 dark:hover:bg-neutral-800"
          >
            <input
              type="checkbox"
              class="size-4 rounded border-neutral-300"
              :checked="chosen.has(c.code)"
              @change="set([c.code], $event.target.checked)"
            />
            <span class="font-mono text-code text-neutral-500">{{ c.code }}</span>
            <span class="truncate">{{ c.name }}</span>
          </label>
        </div>
      </div>
    </div>

    <p v-if="unknown.length" class="text-sm text-neutral-500">
      Also selected, and not a country this box knows:
      <span class="font-mono">{{ unknown.join(', ') }}</span>
    </p>
  </div>
</template>
