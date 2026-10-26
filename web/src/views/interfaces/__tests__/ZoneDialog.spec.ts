import { mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { Zone } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ZoneDialog from '@/views/interfaces/ZoneDialog.vue'
import { configOf, must, sessionOf } from '@/test/fixtures'

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

function open(zone: Zone | null, role: string) {
  useAuthStore().user = sessionOf({ username: role, role })
  useConfigStore().replaceDraft(
    configOf({
      version: 11,
      zones: [{ name: 'lan', antiLockout: true }],
      interfaces: [],
      rules: [],
    }),
  )
  return mount(ZoneDialog, { props: { open: true, zone }, global: { stubs } })
}

const antiLockout = (w: VueWrapper) =>
  must(
    w
      .findAll('input[type="checkbox"]')
      .find((i) => i.element.parentElement?.textContent?.includes('Anti-lockout')),
  )

// Anti-lockout keeps the management ports open, so only an admin may move
// it, and it is kept by zone name, so renaming such a zone moves it too.
describe('ZoneDialog anti-lockout', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps anti-lockout and the zone it holds from an operator', () => {
    const w = open({ name: 'lan', antiLockout: true }, 'operator')
    expect(antiLockout(w).attributes('disabled')).toBeDefined()
    expect(w.get('#zone-name').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('Only an admin can rename a zone with anti-lockout.')
  })

  it('lets an operator add a zone without it', () => {
    const w = open(null, 'operator')
    expect(antiLockout(w).attributes('disabled')).toBeDefined()
    expect(w.get('#zone-name').attributes('disabled')).toBeUndefined()
  })

  it('leaves both to an admin', () => {
    const w = open({ name: 'lan', antiLockout: true }, 'admin')
    expect(antiLockout(w).attributes('disabled')).toBeUndefined()
    expect(w.get('#zone-name').attributes('disabled')).toBeUndefined()
  })
})
