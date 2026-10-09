import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ListContentsDialog from '@/components/ListContentsDialog.vue'
import { useAuthStore } from '@/stores/auth'

vi.mock('@/lib/api', () => ({ api: {}, ApiError: class ApiError extends Error {} }))

const ENTRIES = Array.from({ length: 2500 }, (_, i) => `10.0.${Math.floor(i / 100)}.${i % 100}`)

/** Stands in for the router: searches and pages the way it does. */
function router() {
  return vi.fn(async (q, offset, limit) => {
    const hits = ENTRIES.filter((e) => e.includes(q))
    return {
      total: ENTRIES.length,
      matches: hits.length,
      offset,
      items: hits.slice(offset, offset + limit),
      fetchedAt: '2026-09-26T12:00:00Z',
    }
  })
}

async function opened(read) {
  const wrapper = mount(ListContentsDialog, {
    props: {
      open: true,
      title: 'Entries fetched for drop',
      label: 'Entries',
      placeholder: 'address or network',
      read,
    },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

const rows = () =>
  [...document.querySelectorAll('ul[aria-label="Entries"] li')].map((li) => li.textContent)
const button = (name) =>
  [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === name)
const search = () => document.querySelector('input[type=search]')

describe('ListContentsDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))
  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('reads the first page when it opens', async () => {
    const read = router()
    await opened(read)
    expect(read).toHaveBeenCalledWith('', 0, 1000)
    expect(rows()).toHaveLength(1000)
    expect(rows()[0]).toBe('10.0.0.0')
    expect(document.body.textContent).not.toContain('2,500 of 2,500')
    expect(document.body.textContent).toContain('1–1,000 of 2,500.')
    expect(button('Previous')).toBeUndefined()
  })

  it('pages forward to the end and back', async () => {
    const read = router()
    await opened(read)
    button('Next').click()
    await flushPromises()
    expect(read).toHaveBeenLastCalledWith('', 1000, 1000)
    expect(document.body.textContent).toContain('1,001–2,000 of 2,500.')
    button('Next').click()
    await flushPromises()
    expect(rows()).toHaveLength(500)
    expect(document.body.textContent).toContain('2,001–2,500 of 2,500.')
    expect(button('Next')).toBeUndefined()
    button('Previous').click()
    await flushPromises()
    expect(read).toHaveBeenLastCalledWith('', 1000, 1000)
  })

  it('shows a new page from its top', async () => {
    await opened(router())
    const list = document.querySelector('ul[aria-label="Entries"]')
    list.scrollTop = 5000
    button('Next').click()
    await flushPromises()
    expect(list.scrollTop).toBe(0)
  })

  it('asks the router once typing rests, from the first page', async () => {
    vi.useFakeTimers()
    const read = router()
    await opened(read)
    button('Next').click()
    await flushPromises()
    search().value = '10.0.1.'
    search().dispatchEvent(new Event('input'))
    await flushPromises()
    expect(read).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(250)
    await flushPromises()
    expect(read).toHaveBeenLastCalledWith('10.0.1.', 0, 1000)
    expect(rows()).toHaveLength(100)
    expect(document.body.textContent).toContain('100 of 2,500')

    search().value = 'nowhere'
    search().dispatchEvent(new Event('input'))
    await flushPromises()
    vi.advanceTimersByTime(250)
    await flushPromises()
    expect(rows()).toHaveLength(0)
    expect(document.body.textContent).toContain('Nothing matches "nowhere".')
  })

  it('says Reading… until the first page is in, and why a read failed', async () => {
    await opened(() => new Promise(() => {}))
    expect(document.body.textContent).toContain('Reading…')
    document.body.innerHTML = ''

    await opened(() => Promise.reject(new Error('nothing called "drop" is fetched from anywhere')))
    expect(document.querySelector('[role=alert]').textContent).toContain('nothing called "drop"')
  })

  // Nothing here changes the router, so a viewer searches and pages too.
  it('lets a viewer search and page', async () => {
    useAuthStore().user = { username: 'v', role: 'viewer' }
    await opened(router())
    expect(document.querySelector('fieldset[disabled]')).toBeNull()
    expect(search().disabled).toBe(false)
    expect(button('Next').disabled).toBe(false)
  })
})
