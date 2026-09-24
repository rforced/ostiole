import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ManagementSection from '@/views/system/ManagementSection.vue'

vi.mock('@/lib/api', () => ({ api: { timezones: vi.fn() } }))

const draft = (system = {}, services) => ({
  version: 3,
  system,
  zones: [],
  interfaces: [],
  rules: [],
  ...(services ? { services } : {}),
})

async function open(system, services, role = 'admin') {
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  config.replaceDraft(draft(system, services))
  const wrapper = mount(ManagementSection, { global: { stubs: { RouterLink: true } } })
  await flushPromises()
  return { wrapper, config, zone: wrapper.get('#sys-timezone') }
}

describe('ManagementSection timezone', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.timezones.mockResolvedValue({
      zones: ['UTC', 'Asia/Tokyo', 'Europe/Berlin'],
      current: 'UTC',
    })
  })

  it('shows UTC when the configuration says nothing', async () => {
    const { zone } = await open({})
    expect(zone.element.value).toBe('UTC')
  })

  it('writes a chosen zone into the draft', async () => {
    const { config, zone } = await open({ timezone: 'UTC' })
    await zone.setValue('Europe/Berlin')
    expect(config.draft.system.timezone).toBe('Europe/Berlin')
  })

  // Opening the page is not an edit: a draft that had no timezone still has
  // none until somebody picks one, so the apply bar stays quiet.
  it('does not touch a draft it only displays', async () => {
    const { config } = await open({})
    expect(config.draft.system.timezone).toBeUndefined()
  })

  // A configuration restored from a router with a bigger zone database must
  // not be quietly moved to whichever zone happens to sort next to it.
  it('keeps a zone the router does not offer', async () => {
    const { zone } = await open({ timezone: 'Pacific/Chatham' })
    expect(zone.element.value).toBe('Pacific/Chatham')
    expect(zone.findAll('option').map((o) => o.element.value)).toContain('Pacific/Chatham')
  })

  it('says what the clock reads while the setting is unapplied', async () => {
    const { wrapper } = await open({ timezone: 'Europe/Berlin' })
    expect(wrapper.text()).toContain('The clock reads UTC until you apply.')
  })
})

describe('ManagementSection host settings', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.timezones.mockResolvedValue({ zones: ['UTC'], current: 'UTC' })
  })

  // The field only resolves for the router while the DNS service is off, so
  // the hint has to say which of the three it is rather than let an operator
  // think a dead field is the one their lookups go through.
  it('says these servers resolve for the router when the DNS service is off', async () => {
    const { wrapper } = await open({}, { dns: { enabled: false } })
    expect(wrapper.get('#sys-dns').element.value).toBe('')
    expect(wrapper.text()).toContain('This router resolves names here.')
  })

  it('says a forwarder with no upstreams of its own falls back here', async () => {
    const { wrapper } = await open({}, { dns: { enabled: true } })
    expect(wrapper.text()).toContain('forwards here until it has upstreams of its own')
  })

  it('calls the field unused once the DNS service resolves', async () => {
    const { wrapper } = await open({}, { dns: { enabled: true, resolver: 'tls' } })
    expect(wrapper.text()).toContain('Unused while the DNS service answers for this router.')
  })

  it('calls the field unused once the forwarder has upstreams', async () => {
    const { wrapper } = await open({}, { dns: { enabled: true, upstreams: ['1.1.1.1'] } })
    expect(wrapper.text()).toContain('Unused while the DNS service answers for this router.')
  })

  // How sshd lets people in is configuration like anything else, so the
  // checkbox writes the draft and the apply is what changes the router.
  it('writes the SSH password setting into the draft', async () => {
    const { wrapper, config } = await open({ management: { webPort: 443, sshPort: 22 } })
    const box = wrapper.findAll('input[type="checkbox"]')[0]
    expect(box.element.checked).toBe(false)
    await box.setValue(true)
    expect(config.draft.system.management.sshPasswords).toBe(true)
  })

  // How the router is reached is an admin's to change, and the apply would
  // refuse it from anyone else, so the fields say so up front.
  it('greys out management access for an operator', async () => {
    const { wrapper } = await open(
      { management: { webPort: 443, sshPort: 22 } },
      undefined,
      'operator',
    )
    expect(wrapper.get('#sys-web').attributes('disabled')).toBeDefined()
    expect(wrapper.get('#sys-ssh').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('input[type="checkbox"]')[0].attributes('disabled')).toBeDefined()
    expect(wrapper.get('#sys-hostname').attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).toContain('Only an admin can change this.')
  })

  // A viewer changes nothing, so every field is shown disabled and none
  // of them is singled out as an admin's.
  it('shows a viewer every setting, disabled', async () => {
    const { wrapper } = await open(
      { management: { webPort: 443, sshPort: 22 } },
      undefined,
      'viewer',
    )
    expect(wrapper.find('fieldset[disabled] #sys-hostname').exists()).toBe(true)
    expect(wrapper.find('fieldset[disabled] #sys-web').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('Only an admin')
    expect(wrapper.text()).toContain('Kept open from anti-lockout zones.')
  })
})
