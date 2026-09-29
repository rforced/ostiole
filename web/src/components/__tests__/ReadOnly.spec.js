import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import AppDisclosure from '@/components/AppDisclosure.vue'
import ApplyPending from '@/components/ApplyPending.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useAuthStore } from '@/stores/auth'

vi.mock('@/lib/api', () => ({
  api: { status: vi.fn(), config: { confirm: vi.fn(), revert: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

function as(role) {
  useAuthStore().user = { username: role, role }
}

/** A settings card with a field, a fold and a header action. */
const Settings = defineComponent({
  props: { locked: Boolean },
  setup(props) {
    return () =>
      h(
        SectionCard,
        { title: 'Settings', locked: props.locked },
        {
          actions: () => h('button', { type: 'button', id: 'act' }, 'Refresh'),
          default: () => [
            h('input', { id: 'field' }),
            h(AppDisclosure, null, { default: () => h('input', { id: 'folded' }) }),
          ],
        },
      )
  },
})

describe('read only', () => {
  beforeEach(() => setActivePinia(createPinia()))
  afterEach(() => {
    document.body.innerHTML = ''
  })

  // A locked card shows its settings through a disabled fieldset, and what
  // an Advanced fold holds is shown open: its trigger is disabled with the
  // rest, and would otherwise keep those settings out of reach.
  it('locks a card body but not its header, and opens its folds', async () => {
    const locked = mount(Settings, { props: { locked: true } })
    await flushPromises()
    expect(locked.find('fieldset[disabled] #field').exists()).toBe(true)
    expect(locked.find('fieldset[disabled] #folded').exists()).toBe(true)
    expect(locked.find('fieldset #act').exists()).toBe(false)

    const open = mount(Settings, { props: { locked: false } })
    await flushPromises()
    expect(open.find('fieldset').exists()).toBe(false)
    expect(open.find('#folded').exists()).toBe(false)
  })

  // Every dialog is read only for a viewer unless it says otherwise, so a
  // row's View shows the form with one way out: Close.
  it('shows a viewer a dialog with its fields disabled and one Close', async () => {
    as('viewer')
    mount(AppDialog, {
      props: { open: true, title: 'Rule 12' },
      slots: {
        default: '<form><input id="name" /><div><button type="submit">Save</button></div></form>',
      },
      attachTo: document.body,
    })
    await flushPromises()
    const fieldset = document.querySelector('fieldset.read-only')
    expect(fieldset?.disabled).toBe(true)
    expect(fieldset.querySelector('#name')).not.toBeNull()
    const close = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Close')
    expect(close).toBeDefined()
    expect(fieldset.contains(close)).toBe(false)
  })

  it('leaves a dialog alone for an operator, and one that says so for a viewer', async () => {
    as('operator')
    mount(AppDialog, {
      props: { open: true, title: 'Rule 12' },
      slots: { default: '<input id="name" />' },
      attachTo: document.body,
    })
    await flushPromises()
    expect(document.querySelector('fieldset')).toBeNull()
    document.body.innerHTML = ''

    as('viewer')
    mount(AppDialog, {
      props: { open: true, title: 'Delete it?', readOnly: false },
      slots: { default: '<input id="typed" />' },
      attachTo: document.body,
    })
    await flushPromises()
    expect(document.querySelector('fieldset')).toBeNull()
    expect(document.querySelector('#typed')).not.toBeNull()
  })

  it('leaves destructive actions out for a viewer', () => {
    as('viewer')
    const w = mount(ConfirmButton, { props: { label: 'Delete', question: 'Delete it?' } })
    expect(w.find('button').exists()).toBe(false)
    as('operator')
    const o = mount(ConfirmButton, { props: { label: 'Delete', question: 'Delete it?' } })
    expect(o.find('button').exists()).toBe(true)
  })

  // A viewer sees that an apply waits, and cannot be the one to settle it.
  it('shows a viewer a waiting apply without Confirm or Revert', () => {
    as('viewer')
    const deadline = new Date(Date.now() + 60_000).toISOString()
    const w = mount(ApplyPending, { props: { deadline } })
    expect(w.text()).toContain('Unless an operator confirms them')
    expect(w.findAll('button')).toHaveLength(0)
    w.unmount()
  })
})
