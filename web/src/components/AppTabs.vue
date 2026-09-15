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
    <TabsList
      class="mb-4 flex gap-1 border-b border-neutral-200 dark:border-neutral-800"
      aria-label="Sections"
    >
      <TabsTrigger
        v-for="t in tabs"
        :key="t.value"
        :value="t.value"
        class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-neutral-500 hover:text-neutral-900 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:outline-none data-[state=active]:border-sky-600 data-[state=active]:font-medium data-[state=active]:text-neutral-900 dark:hover:text-neutral-100 dark:data-[state=active]:border-sky-400 dark:data-[state=active]:text-neutral-100"
      >
        {{ t.label }}
      </TabsTrigger>
    </TabsList>
    <slot />
  </TabsRoot>
</template>
