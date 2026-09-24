<script setup>
import { computed, useId, useSlots } from 'vue'

import { provideLocked } from '@/lib/locked'

/**
 * One card of a page. The header is the title, a count, an intro under
 * it, and the actions slot on the right. The body is a table that runs
 * to the edges (flush), fields, or a key-value list. A card with nothing
 * to head has no header.
 */
const props = defineProps({
  title: { type: String, default: '' },
  intro: { type: String, default: '' },
  /** After the title: how many rows the list has. Zero stays quiet. */
  count: { type: Number, default: null },
  /** The body is a table and runs to the edges. */
  flush: { type: Boolean, default: false },
  /** The element; a card that submits is a form. */
  as: { type: String, default: 'section' },
  /** The body is settings the account may not change: shown, disabled. */
  locked: { type: Boolean, default: false },
})

const slots = useSlots()
const id = useId()
const headed = computed(() => Boolean(props.title || slots.title || slots.actions))
provideLocked(() => props.locked)
</script>

<template>
  <component
    :is="as"
    class="overflow-hidden rounded-lg border border-line bg-surface text-sm"
    :aria-labelledby="title || $slots.title ? id : undefined"
  >
    <div
      v-if="headed"
      class="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 px-4 pt-4 pb-3"
    >
      <div class="min-w-0">
        <!-- The count sits outside the label, so the card's name in the
             accessibility tree is the title alone and does not change
             every time a row is added. -->
        <h2 class="card-title flex items-center gap-2">
          <span :id="id" class="flex items-center gap-2">
            <slot name="title">{{ title }}</slot>
          </span>
          <span v-if="count" class="font-normal text-ink-muted tabular-nums">
            {{ count }}
          </span>
        </h2>
        <p v-if="intro || $slots.intro" class="mt-0.5 max-w-3xl text-ink-muted">
          <slot name="intro">{{ intro }}</slot>
        </p>
      </div>
      <div v-if="$slots.actions" class="flex flex-wrap items-center gap-2">
        <slot name="actions" />
      </div>
    </div>
    <!-- Relative below lg, so nothing absolute in the table (sr-only text)
         escapes the scroller and widens the page. Not above it: a positioned
         scroller loses subpixel text on a desktop. -->
    <div
      v-if="flush"
      class="overflow-x-auto max-lg:relative max-lg:scroll-fade"
      :class="{ 'border-t border-line': headed }"
    >
      <slot />
    </div>
    <div v-else-if="$slots.default" :class="headed ? 'px-4 pb-4' : 'p-4'">
      <fieldset v-if="locked" disabled class="min-w-0">
        <slot />
      </fieldset>
      <slot v-else />
    </div>
  </component>
</template>
