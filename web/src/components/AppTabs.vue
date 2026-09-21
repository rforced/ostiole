<script setup>
import { TabsList, TabsRoot, TabsTrigger } from 'reka-ui'

defineProps({
  /** @type {import('vue').PropType<{value: string, label: string}[]>} */
  tabs: { type: Array, required: true },
})
const active = defineModel({ type: String, required: true })
</script>

<template>
  <TabsRoot v-model="active">
    <TabsList class="mb-5 flex gap-1 border-b border-line" aria-label="Sections">
      <TabsTrigger
        v-for="t in tabs"
        :key="t.value"
        :value="t.value"
        class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-ink-muted hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none data-[state=active]:border-accent data-[state=active]:font-medium data-[state=active]:text-ink"
      >
        {{ t.label }}
      </TabsTrigger>
    </TabsList>
    <slot />
  </TabsRoot>
</template>
