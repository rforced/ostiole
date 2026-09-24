<script setup>
import { computed, useId, useSlots } from 'vue'

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
})

const slots = useSlots()
const id = useId()
const headed = computed(() => Boolean(props.title || slots.title || slots.actions))
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
    <div v-if="flush" class="overflow-x-auto" :class="{ 'border-t border-line': headed }">
      <slot />
    </div>
    <div v-else-if="$slots.default" :class="headed ? 'px-4 pb-4' : 'p-4'">
      <slot />
    </div>
  </component>
</template>
