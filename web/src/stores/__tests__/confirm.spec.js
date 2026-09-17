import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfirmStore } from '@/stores/confirm'

describe('confirm store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('resolves with the answer and clears the request', async () => {
    const confirm = useConfirmStore()
    const answer = confirm.ask({ question: 'Delete rule r1?' })
    expect(confirm.request?.question).toBe('Delete rule r1?')
    expect(confirm.request?.confirmLabel).toBe('Delete')
    confirm.settle(true)
    await expect(answer).resolves.toBe(true)
    expect(confirm.request).toBeNull()
  })

  it('answers no when closed any other way', async () => {
    const confirm = useConfirmStore()
    const answer = confirm.ask({ question: 'Reboot?', confirmLabel: 'Reboot', typed: 'reboot' })
    expect(confirm.request?.typed).toBe('reboot')
    confirm.settle(false)
    await expect(answer).resolves.toBe(false)
  })

  it('answers an open question with no when another is asked', async () => {
    const confirm = useConfirmStore()
    const first = confirm.ask({ question: 'First?' })
    const second = confirm.ask({ question: 'Second?' })
    await expect(first).resolves.toBe(false)
    expect(confirm.request?.question).toBe('Second?')
    confirm.settle(true)
    await expect(second).resolves.toBe(true)
  })
})
