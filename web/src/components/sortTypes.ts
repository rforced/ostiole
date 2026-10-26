import type { Ref } from 'vue'

/** The part of what useSort returns that the sort header and select use. */
export interface SortControl {
  order: Ref<{ by: string | null; dir: string | null }>
  toggle(key: string | string[]): void
  choose(key: string): void
}

/** A key and the label shown for it, as a pair; loose so a constant array of pairs fits. */
export type SortColumn = string[]
