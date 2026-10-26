import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useToastStore } from '@/stores/toast'

describe('toast store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
  })
  afterEach(() => vi.useRealTimers())

  it('shows a toast and takes it away after its timeout', () => {
    const toast = useToastStore()
    toast.show('Deleted rule r1.', { timeout: 1000 })
    expect(toast.toasts.map((t) => t.message)).toEqual(['Deleted rule r1.'])
    vi.advanceTimersByTime(999)
    expect(toast.toasts).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(toast.toasts).toHaveLength(0)
  })

  it('runs the action once and dismisses', () => {
    const toast = useToastStore()
    const run = vi.fn()
    const id = toast.show('Deleted.', { action: { label: 'Undo', run } })
    toast.act(id)
    expect(run).toHaveBeenCalledTimes(1)
    expect(toast.toasts).toHaveLength(0)
    // Acting on a toast that is gone does nothing.
    toast.act(id)
    expect(run).toHaveBeenCalledTimes(1)
  })

  it('keeps only the newest few', () => {
    const toast = useToastStore()
    for (let i = 1; i <= 6; i++) toast.show(`toast ${i}`, { timeout: 0 })
    expect(toast.toasts.map((t) => t.message)).toEqual(['toast 3', 'toast 4', 'toast 5', 'toast 6'])
  })
})
