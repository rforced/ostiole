import type { VueWrapper } from '@vue/test-utils'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import ZoneField from '@/components/ZoneField.vue'
import type { Role, Session } from '@/lib/api'
import type { Config } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { fixture } from '@/test/fixtures'

function field(iface: string, zone: string, role: Role) {
  useAuthStore().user = { username: role, role } as Session
  useConfigStore().replaceDraft(
    fixture<Config>({
      version: 12,
      zones: [{ name: 'wan', external: true }, { name: 'lan', antiLockout: true }, { name: 'dmz' }],
      interfaces: [
        { name: 'eth0', zone: 'wan', enabled: true },
        { name: 'eth1', zone: 'lan', enabled: true },
      ],
      rules: [],
    }),
  )
  return mount(ZoneField, { props: { id: 'z', iface, modelValue: zone } })
}

const option = (w: VueWrapper, name: string) => {
  const o = w.findAll<HTMLOptionElement>('option').find((o) => o.element.value === name)
  if (!o) throw new Error(`no option ${name}`)
  return o
}

// The interfaces in an anti-lockout zone are where the router is managed
// from, which is an admin's call.
describe('ZoneField and anti-lockout', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps an operator from moving the wan into an anti-lockout zone', () => {
    const w = field('eth0', 'wan', 'operator')
    expect(option(w, 'lan').attributes('disabled')).toBeDefined()
    expect(option(w, 'dmz').attributes('disabled')).toBeUndefined()
    expect(w.get('select').attributes('disabled')).toBeUndefined()
    expect(w.text()).toContain(
      'Only an admin can move an interface into or out of a zone with anti-lockout.',
    )
  })

  it('keeps an operator from moving the lan out of one', () => {
    expect(field('eth1', 'lan', 'operator').get('select').attributes('disabled')).toBeDefined()
  })

  it('keeps a new interface out of one for an operator', () => {
    expect(option(field('eth2', '', 'operator'), 'lan').attributes('disabled')).toBeDefined()
  })

  it('leaves it all to an admin', () => {
    const w = field('eth1', 'lan', 'admin')
    expect(w.get('select').attributes('disabled')).toBeUndefined()
    expect(option(w, 'dmz').attributes('disabled')).toBeUndefined()
    expect(w.text()).not.toContain('Only an admin')
  })
})
