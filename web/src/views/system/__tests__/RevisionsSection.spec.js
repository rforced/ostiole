import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import RevisionsSection from '@/views/system/RevisionsSection.vue'

vi.mock('@/lib/api', () => ({
  api: { config: { revisions: vi.fn(), revision: vi.fn(), diff: vi.fn() } },
}))

const draft = (system = {}) => ({ version: 2, system, zones: [], interfaces: [], rules: [] })

function open(system) {
  const config = useConfigStore()
  config.replaceDraft(draft(system))
  const wrapper = mount(RevisionsSection, { global: { stubs: { ChangeList: true } } })
  return { wrapper, config, keep: wrapper.get('#rev-keep') }
}

describe('RevisionsSection retention', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.config.revisions.mockResolvedValue([])
  })

  it('shows the default when the configuration sets none', () => {
    expect(open({}).keep.element.value).toBe('20')
  })

  it('writes a changed limit into the draft', async () => {
    const { config, keep } = open({})
    await keep.setValue(5)
    expect(config.draft.system.keepRevisions).toBe(5)
  })

  // An empty field and the default mean the same thing, and neither belongs
  // in the configuration: "" would not even unmarshal as a number.
  it('leaves the setting out for the default or an empty field', async () => {
    const { config, keep } = open({ keepRevisions: 5 })
    await keep.setValue(20)
    expect(config.draft.system.keepRevisions).toBeUndefined()
    await keep.setValue(5)
    await keep.setValue('')
    expect(config.draft.system.keepRevisions).toBeUndefined()
  })

  // Loading a revision or a backup replaces the draft wholesale; the field
  // has to follow it rather than keep showing the old number.
  it('follows a draft that is replaced', async () => {
    const { config, keep } = open({ keepRevisions: 5 })
    expect(keep.element.value).toBe('5')
    config.replaceDraft(draft({ keepRevisions: 50 }))
    await nextTick()
    expect(keep.element.value).toBe('50')
  })
})
