import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import MfaBypassNotice from '@/components/MfaBypassNotice.vue'
import type { Session } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'

describe('MfaBypassNotice', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('says when the command line has bypassed MFA, and who did', () => {
    useAuthStore().user = {
      username: 'sam',
      role: 'admin',
      bypassUntil: '2099-10-10T12:00:00Z',
      bypassBy: 'root',
    } as Session
    const note = mount(MfaBypassNotice).get('[role="note"]').text()
    expect(note).toMatch(/^MFA is bypassed until .+ by root, from the command line\.$/)
  })

  it('leaves out who when the router does not know', () => {
    useAuthStore().user = {
      username: 'sam',
      role: 'admin',
      bypassUntil: '2099-10-10T12:00:00Z',
    } as Session
    const note = mount(MfaBypassNotice).get('[role="note"]').text()
    expect(note).toMatch(/^MFA is bypassed until .+, from the command line\.$/)
    expect(note).not.toContain(' by ')
  })

  it('shows nothing without a bypass', () => {
    useAuthStore().user = { username: 'sam', role: 'admin' } as Session
    expect(mount(MfaBypassNotice).find('[role="note"]').exists()).toBe(false)
  })
})
