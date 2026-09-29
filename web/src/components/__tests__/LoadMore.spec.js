import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import LoadMore from '@/components/LoadMore.vue'

/** An observer the test says the foot is in view through. */
let observer = null
class FakeObserver {
  constructor(callback) {
    this.callback = callback
    this.observed = 0
    observer = this
  }
  observe() {
    this.observed++
  }
  unobserve() {}
  disconnect() {}
  inView(yes = true) {
    this.callback([{ isIntersecting: yes }])
  }
}

describe('LoadMore', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('reads on from its button, and says where the log starts', async () => {
    const w = mount(LoadMore, { props: { more: true, rows: 200 } })
    await w.get('button').trigger('click')
    expect(w.emitted('load')).toHaveLength(1)
    await w.setProps({ more: false })
    expect(w.find('button').exists()).toBe(false)
    expect(w.text()).toBe('Start of the log.')
    // An empty table says nothing here; the table says it.
    await w.setProps({ rows: 0 })
    expect(w.text()).toBe('')
  })

  it('says how far back a search looked', () => {
    const w = mount(LoadMore, {
      props: { more: true, rows: 3, searchedTo: '2026-09-20T12:00:00Z' },
    })
    expect(w.text()).toMatch(/Searched back to .+\./)
    expect(w.text()).not.toContain('Start of the log.')
  })

  // Scrolling the foot into view reads the next page, and a page that
  // leaves it in view reads on once it is in.
  it('reads on as it comes into view', async () => {
    vi.stubGlobal('IntersectionObserver', FakeObserver)
    const w = mount(LoadMore, { props: { more: true, rows: 200 } })
    observer.inView()
    expect(w.emitted('load')).toHaveLength(1)
    await w.setProps({ busy: true })
    observer.inView()
    expect(w.emitted('load')).toHaveLength(1)
    await w.setProps({ busy: false })
    await nextTick()
    expect(observer.observed).toBe(2)
    observer.inView(false)
    expect(w.emitted('load')).toHaveLength(1)
  })
})
