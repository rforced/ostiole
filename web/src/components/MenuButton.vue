<script setup>
import { ChevronDown } from 'lucide-vue-next'
import {
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuRoot,
  DropdownMenuTrigger,
} from 'reka-ui'
import { ref } from 'vue'

/**
 * A row action that lists several of one kind when pressed, such as the
 * files of one certificate. Edit, Delete and anything that spins stay in
 * the row. An item is a link, `{ label, href }`, or an action,
 * `{ label, action }`.
 */
defineOptions({ inheritAttrs: false })
defineProps({
  label: { type: String, required: true },
  items: { type: Array, required: true },
})

const trigger = ref(null)
// An action runs once the menu has closed, with focus back on the trigger.
// A dialog it opens returns focus to whatever had it, and the item that
// was clicked is gone by then.
let chosen = null

function choose(item) {
  chosen = item.action ?? null
}

function closed(event) {
  if (!chosen) return
  event.preventDefault()
  trigger.value?.focus()
  const run = chosen
  chosen = null
  run()
}
</script>

<template>
  <DropdownMenuRoot>
    <DropdownMenuTrigger as-child>
      <button
        ref="trigger"
        type="button"
        class="link inline-flex items-center gap-0.5 aria-expanded:underline"
        v-bind="$attrs"
      >
        {{ label }}<ChevronDown class="size-4" aria-hidden="true" />
      </button>
    </DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent
        align="end"
        :side-offset="4"
        :collision-padding="8"
        class="z-50 min-w-36 rounded-md border border-line bg-surface p-1 shadow-lg focus:outline-none"
        @close-auto-focus="closed"
      >
        <DropdownMenuItem
          v-for="item in items"
          :key="item.label"
          :as="item.href ? 'a' : 'div'"
          :href="item.href"
          class="flex cursor-default items-center rounded px-2.5 py-1.5 text-sm text-ink outline-none select-none data-highlighted:bg-accent data-highlighted:text-on-fill max-sm:min-h-11"
          @select="choose(item)"
        >
          {{ item.label }}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>
