import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent, h, nextTick, onUpdated, ref } from 'vue'

import { useReorder } from '@/lib/reorder'

/** A list that notes the move class each update of it rendered with. */
const Host = defineComponent({
  setup() {
    const items = ref(['a', 'b'])
    const seen = []
    const { moveClass, reorder } = useReorder()
    onUpdated(() => seen.push(`${items.value.join('')} ${moveClass.value}`))
    return { items, seen, moveClass, reorder }
  },
  render() {
    return h(
      'ul',
      { class: this.moveClass },
      this.items.map((i) => h('li', { key: i }, i)),
    )
  },
})

describe('useReorder', () => {
  it('slides the update a reorder causes, and no other', async () => {
    const { vm } = mount(Host)
    vm.reorder(() => vm.items.reverse())
    await flushPromises()
    // The reorder rendered with the slide, then the class went back.
    expect(vm.seen).toEqual(['ba row-move', 'ba row-still'])

    vm.items.push('c')
    await nextTick()
    expect(vm.seen.at(-1)).toBe('bac row-still')
  })
})
