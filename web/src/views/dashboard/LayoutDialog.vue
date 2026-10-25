<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import { useDragSort } from '@/lib/dragSort'
import { CARDS, firstLayout, isCustomized } from '@/views/dashboard/layout'
import LayoutRow from '@/views/dashboard/LayoutRow.vue'

const props = defineProps({
  /** The layout the dashboard draws, { order, hidden }. */
  layout: { type: Object, required: true },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['change'])

const copy = (layout) => ({ order: [...layout.order], hidden: [...layout.hidden] })

/** The rows' own copy, so a drag moves rows before it saves. */
const rows = ref(copy(props.layout))

function save(next) {
  rows.value = next
  emit('change', copy(next))
}

const list = ref(null)
const order = computed({
  get: () => rows.value.order,
  set: (next) => (rows.value = { ...rows.value, order: next }),
})
const { dragging, moveClass, grab, stop } = useDragSort(list, order, () => save(rows.value))

watch(open, (on) => {
  stop()
  if (on) rows.value = copy(props.layout)
})

function toggle(name) {
  const { hidden } = rows.value
  save({
    ...rows.value,
    hidden: hidden.includes(name) ? hidden.filter((n) => n !== name) : [...hidden, name],
  })
}

const titles = Object.fromEntries(CARDS.map((c) => [c.name, c.title]))
const customized = computed(() => isCustomized(rows.value))
</script>

<template>
  <AppDialog
    v-model:open="open"
    title="Dashboard layout"
    description="Drag a card by its handle to move it, or switch it off. Only this browser keeps the layout."
    :read-only="false"
  >
    <ul ref="list" :class="{ 'cursor-grabbing select-none': dragging }">
      <TransitionGroup :css="false" :move-class="moveClass">
        <LayoutRow
          v-for="name in rows.order"
          :key="name"
          :title="titles[name]"
          :shown="!rows.hidden.includes(name)"
          :dragging="dragging === name"
          @toggle="toggle(name)"
          @grab="grab($event, name)"
        />
      </TransitionGroup>
    </ul>
    <template #footer>
      <button v-if="customized" type="button" class="btn-secondary" @click="save(firstLayout())">
        Restore
      </button>
      <button type="button" class="btn-secondary" @click="open = false">Close</button>
    </template>
  </AppDialog>
</template>
