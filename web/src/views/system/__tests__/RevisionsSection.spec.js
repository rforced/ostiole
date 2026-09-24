import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import RevisionsSection from '@/views/system/RevisionsSection.vue'

vi.mock('@/lib/api', () => ({
  api: { config: { revisions: vi.fn(), revision: vi.fn(), diff: vi.fn() } },
}))

const draft = (system = {}) => ({ version: 3, system, zones: [], interfaces: [], rules: [] })

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

describe('RevisionsSection compare', () => {
  const revisions = [
    { id: '20260922T040539.652337697Z', time: '2026-09-22T04:05:39Z', size: 8108 },
    { id: '20260922T034544.783025712Z', time: '2026-09-22T03:45:44Z', size: 7958 },
    { id: '20260922T010707.523171227Z', time: '2026-09-22T01:07:07Z', size: 7988 },
  ]

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.config.revisions.mockResolvedValue(revisions)
    api.config.diff.mockResolvedValue([])
  })

  // The body as the reader sees it: a revision's id, or "changes" for the
  // row that spans the table, and a cell count that has to fit the header.
  function body(wrapper) {
    return wrapper.findAll('tbody tr').map((tr) => {
      const cells = tr.findAll('td')
      if (cells.length === 1 && cells[0].attributes('colspan') === '4') return 'changes'
      const id = revisions.find((r) => tr.text().includes(r.id))?.id
      return cells.length === 4 && !cells[0].attributes('colspan') ? id : `broken: ${tr.html()}`
    })
  }

  // Opening one compare after another used to stitch a revision into the
  // changes row of another: in the transition group both rows of a
  // revision had the same key, so Vue patched one onto the other.
  it('keeps every row whole as compares open and close', async () => {
    const config = useConfigStore()
    config.replaceDraft(draft({}))
    const wrapper = mount(RevisionsSection, {
      global: { stubs: { ChangeList: true, 'transition-group': false } },
    })
    await flushPromises()
    const [a, b, c] = revisions.map((r) => r.id)
    const settle = async () => {
      await flushPromises()
      await new Promise((resolve) => setTimeout(resolve, 50))
    }
    async function toggle(id) {
      const row = wrapper.findAll('tbody tr').find((tr) => tr.text().includes(id))
      await row.find('button').trigger('click')
      await settle()
    }

    await toggle(b)
    expect(body(wrapper)).toEqual([a, b, 'changes', c])
    await toggle(a)
    expect(body(wrapper)).toEqual([a, 'changes', b, c])
    await toggle(c)
    expect(body(wrapper)).toEqual([a, b, c, 'changes'])
    await toggle(b)
    expect(body(wrapper)).toEqual([a, b, 'changes', c])
    await toggle(b)
    expect(body(wrapper)).toEqual([a, b, c])
  })
})
