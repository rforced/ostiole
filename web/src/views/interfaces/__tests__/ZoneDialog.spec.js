import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ZoneDialog from '@/views/interfaces/ZoneDialog.vue'

const stubs = {
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
}

function open(zone, role) {
  useAuthStore().user = { username: role, role }
  useConfigStore().replaceDraft({
    version: 7,
    zones: [{ name: 'lan', antiLockout: true }],
    interfaces: [],
    rules: [],
  })
  return mount(ZoneDialog, { props: { open: true, zone }, global: { stubs } })
}

const antiLockout = (w) =>
  w
    .findAll('input[type="checkbox"]')
    .find((i) => i.element.parentElement.textContent.includes('Anti-lockout'))

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
