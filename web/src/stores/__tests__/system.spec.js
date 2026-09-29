import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useSystemStore } from '@/stores/system'

describe('system store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.restoreAllMocks()
  })

  it('reads the engine status', async () => {
    vi.spyOn(api, 'status').mockResolvedValue({ configured: true })
    const system = useSystemStore()
    expect(system.status).toBe(null)
    await system.refresh()
    expect(system.status.configured).toBe(true)
    expect(system.error).toBe('')
  })

  // A status that cannot be read is an error on the page, not a null the
  // guard would read as "unconfigured" and act on.
  it('keeps the last status and reports why a refresh failed', async () => {
    vi.spyOn(api, 'status').mockResolvedValue({ configured: true })
    const system = useSystemStore()
    await system.refresh()
    vi.spyOn(api, 'status').mockRejectedValue(new Error('403'))
    await system.refresh()
    expect(system.status.configured).toBe(true)
    expect(system.error).toBe('403')
  })

  it('forgets the status on reset, so a new session re-reads it', async () => {
    vi.spyOn(api, 'status').mockResolvedValue({ configured: true })
    const system = useSystemStore()
    await system.refresh()
    system.reset()
    expect(system.status).toBe(null)
  })
})
