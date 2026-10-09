import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import BlockListDialog from '@/views/services/dns/BlockListDialog.vue'

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual('@/lib/api')
  return {
    ...actual,
    api: { ...actual.api, blocking: { ...actual.api.blocking, catalog: vi.fn() } },
  }
})

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

describe('BlockListDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.mocked(api.blocking.catalog).mockResolvedValue([])
  })

  it('reopens a list as saved and leaves an untouched save alone', async () => {
    const config = useConfigStore()
    const cfg = {
      version: 12,
      zones: [],
      interfaces: [],
      rules: [],
      blocking: {
        enabled: true,
        lists: [{ name: 'ads', enabled: true, url: 'https://lists.example.net/ads.txt' }],
        enforce: {},
      },
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(BlockListDialog, {
      props: { open: true, list: config.blockLists[0] },
      global: { stubs },
    })
    await flushPromises()
    expect(wrapper.get('#bl-refresh').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
