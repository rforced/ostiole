import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'

describe('ConfirmButton', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    useAuthStore().user = { username: 'admin', role: 'admin' }
  })

  it('emits confirm only when the dialog says yes', async () => {
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    const w = mount(ConfirmButton, {
      props: { label: 'Delete', question: 'Delete it?', typed: 'it' },
    })
    await w.get('button').trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({ question: 'Delete it?', confirmLabel: 'Delete', typed: 'it' }),
    )
    expect(w.emitted('confirm')).toBeUndefined()

    ask.mockResolvedValue(true)
    await w.get('button').trigger('click')
    await flushPromises()
    expect(w.emitted('confirm')).toHaveLength(1)
  })

  it('is disabled and spins while busy', async () => {
    const w = mount(ConfirmButton, {
      props: { label: 'Delete all', question: 'Delete all?', busy: true, busyLabel: 'Deleting…' },
    })
    const b = w.get('button')
    expect(b.attributes('disabled')).toBeDefined()
    expect(b.attributes('aria-busy')).toBe('true')
    expect(b.find('.animate-spin').exists()).toBe(true)
    expect(b.text()).toBe('Deleting…')

    await w.setProps({ busy: false })
    expect(b.attributes('disabled')).toBeUndefined()
    expect(b.find('.animate-spin').exists()).toBe(false)
    expect(b.text()).toBe('Delete all')
  })

  it('keeps its label while busy when it has no other', () => {
    const w = mount(ConfirmButton, { props: { label: 'Delete', question: 'Delete?', busy: true } })
    expect(w.get('button').text()).toBe('Delete')
  })

  it('stays disabled when told to, and asks nothing', async () => {
    const ask = vi.spyOn(useConfirmStore(), 'ask')
    const w = mount(ConfirmButton, {
      props: { label: 'Clear', question: 'Clear?', disabled: true },
      attrs: { title: 'Only an admin can clear them.' },
    })
    const b = w.get('button')
    expect(b.attributes('disabled')).toBeDefined()
    expect(b.attributes('title')).toBe('Only an admin can clear them.')
    await b.trigger('click')
    expect(ask).not.toHaveBeenCalled()
  })
})
