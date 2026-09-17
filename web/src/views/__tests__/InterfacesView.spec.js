import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import InterfacesView from '@/views/InterfacesView.vue'

// The page only needs its tabs to exist; the router is not what is under
// test here.
vi.mock('@/lib/tabs', () => ({
  usePageTabs: () => ({ tabs: [], tab: ref('interfaces') }),
}))

vi.mock('@/lib/api', () => ({
  api: {
    interfaces: { live: vi.fn() },
    services: { status: vi.fn() },
    config: { get: vi.fn() },
  },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  AppTabs: true,
  TabsContent: true,
  ConfirmButton: true,
  InterfaceDialog: true,
  VlanDialog: true,
  PppoeDialog: true,
  AggregateDialog: true,
  ZoneDialog: true,
  RouterLink: true,
}

describe('InterfacesView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.interfaces.live.mockResolvedValue([])
    api.services.status.mockResolvedValue({ pppoeSetUp: true })
    api.config.get.mockResolvedValue({ version: 3, interfaces: [], zones: [] })
  })

  // An apply creates and destroys real devices. Without this the table
  // keeps showing a VLAN that the apply just removed from the kernel.
  it('re-reads the live links after an apply', async () => {
    mount(InterfacesView, { global: { stubs } })
    await nextTick()
    expect(api.interfaces.live).toHaveBeenCalledTimes(1)

    useConfigStore().markApplied()
    // The re-read queues behind the first read if that is still in flight.
    await vi.waitFor(() => expect(api.interfaces.live).toHaveBeenCalledTimes(2))
  })
})
