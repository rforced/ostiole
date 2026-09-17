import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useSystemStore } from '@/stores/system'

describe('system store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.restoreAllMocks()
  })

  it('reads whether this router is prepared', async () => {
    vi.spyOn(api.host, 'status').mockResolvedValue({ prepared: false, steps: [] })
    const system = useSystemStore()
    expect(system.host).toBe(null)
    await system.refreshHost()
    expect(system.host.prepared).toBe(false)
  })

  // The gate must never be the thing that stops somebody reaching the UI
  // to fix whatever is wrong with the router.
  it('treats a router that cannot answer as prepared', async () => {
    vi.spyOn(api.host, 'status').mockRejectedValue(new Error('403'))
    const system = useSystemStore()
    await system.refreshHost()
    expect(system.host).toEqual({ prepared: true })
  })

  it('forgets the host report on reset, so a new session re-reads it', async () => {
    vi.spyOn(api.host, 'status').mockResolvedValue({ prepared: true })
    const system = useSystemStore()
    await system.refreshHost()
    system.reset()
    expect(system.host).toBe(null)
    expect(system.status).toBe(null)
  })
})
