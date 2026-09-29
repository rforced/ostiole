import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ClearLogButton from '@/components/ClearLogButton.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

describe('ClearLogButton', () => {
  let ask
  beforeEach(() => {
    setActivePinia(createPinia())
    useAuthStore().user = { username: 'root', role: 'admin' }
    useConfigStore().saved = { system: {} }
    ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
  })

  async function click(props) {
    const w = mount(ClearLogButton, { props })
    await w.get('button').trigger('click')
    await flushPromises()
    return w
  }

  it('asks by the log’s name and says what goes', async () => {
    const w = await click({ name: 'DHCP log', noun: 'message', journal: true })
    expect(w.get('button').text()).toBe('Clear')
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({
        question: 'Clear the DHCP log?',
        description: 'Every message it holds is dropped. The journal keeps its own copy.',
        confirmLabel: 'Clear',
      }),
    )
    expect(w.emitted('confirm')).toHaveLength(1)
  })

  it('says the files go while the router writes them', async () => {
    useConfigStore().saved = { system: { logging: { files: { enabled: true } } } }
    await click({ name: 'drive history', noun: 'reading' })
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({
        description: 'Every reading it holds is dropped, and its files are deleted.',
      }),
    )
  })

  it('takes a description of its own for a Clear that takes more', async () => {
    await click({ name: 'query log', description: 'Every answer it holds is dropped.' })
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({ description: 'Every answer it holds is dropped.' }),
    )
  })

  it('is greyed for an operator, and asks nothing', async () => {
    useAuthStore().user = { username: 'ops', role: 'operator' }
    const w = await click({ name: 'firewall log', noun: 'packet' })
    const b = w.get('button')
    expect(b.attributes('disabled')).toBeDefined()
    expect(b.attributes('title')).toBe('Only an admin can clear it.')
    expect(ask).not.toHaveBeenCalled()
  })

  it('is not there for a viewer', () => {
    useAuthStore().user = { username: 'look', role: 'viewer' }
    const w = mount(ClearLogButton, { props: { name: 'firewall log', noun: 'packet' } })
    expect(w.find('button').exists()).toBe(false)
  })
})
