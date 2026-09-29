import { nextTick, ref } from 'vue'

/** The class main.css gives the slide. */
const MOVE = 'row-move'
/** A class with no transition, so the group moves nothing. */
const STILL = 'row-still'

/**
 * Rows change in the frame their data does; a reorder alone slides, so the
 * eye can follow the row that moved. The table's TransitionGroup takes
 * `:css="false"`, which drops a row that goes at once instead of two
 * frames later, and `:move-class="moveClass"`. The move button makes its
 * change inside `reorder`. An add, a delete or a re-read that shifts rows
 * lands in place.
 *
 * @returns {{moveClass: import('vue').Ref<string>, reorder: (change: () => void) => void}}
 */
export function useReorder() {
  const moveClass = ref(STILL)
  function reorder(change) {
    moveClass.value = MOVE
    change()
    // The group reads the class in the update this change causes; the
    // slide it starts runs on after the class goes back.
    nextTick(() => {
      moveClass.value = STILL
    })
  }
  return { moveClass, reorder }
}
