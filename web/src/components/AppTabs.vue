<script setup lang="ts">
import { TabsList, TabsRoot, TabsTrigger } from 'reka-ui'
import type { PropType } from 'vue'
import { nextTick, onMounted, ref, watch } from 'vue'

defineProps({
  tabs: { type: Array as PropType<{ value: string; label: string }[]>, required: true },
})
const active = defineModel({ type: String, required: true })

/**
 * On a phone the strip scrolls sideways rather than wrap, runs to the
 * edges of the screen, and keeps the open tab in view.
 */
const list = ref<InstanceType<typeof TabsList> | null>(null)
function reveal() {
  const strip = list.value?.$el as HTMLElement | undefined
  const tab = strip?.querySelector('[data-state="active"]')
  if (!strip || !tab || strip.scrollWidth <= strip.clientWidth) return
  const a = strip.getBoundingClientRect()
  const b = tab.getBoundingClientRect()
  strip.scrollLeft += b.left - a.left - (a.width - b.width) / 2
}
onMounted(reveal)
watch(active, () => nextTick(reveal))
</script>

<template>
  <TabsRoot v-model="active">
    <TabsList
      ref="list"
      class="mb-5 flex gap-1 border-b border-line max-sm:-mx-4 max-sm:overflow-x-auto max-sm:border-b-0 max-sm:px-4 max-sm:shadow-[inset_0_-1px_0_var(--line)] max-sm:[--fade:var(--page)] max-sm:[scrollbar-width:none] max-sm:scroll-fade"
      aria-label="Sections"
    >
      <TabsTrigger
        v-for="t in tabs"
        :key="t.value"
        :value="t.value"
        class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-ink-muted hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none data-[state=active]:border-accent data-[state=active]:font-medium data-[state=active]:text-ink max-sm:mb-0 max-sm:shrink-0 max-sm:py-3 max-sm:whitespace-nowrap max-sm:focus-visible:ring-inset"
      >
        {{ t.label }}
      </TabsTrigger>
    </TabsList>
    <slot />
  </TabsRoot>
</template>
