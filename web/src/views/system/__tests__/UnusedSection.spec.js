import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import { useToastStore } from '@/stores/toast'
import UnusedSection from '@/views/system/UnusedSection.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: { config: { unused: vi.fn(), diff: vi.fn().mockResolvedValue([]) } },
}))

function draft() {
  return {
    version: 14,
    zones: [{ name: 'lan' }, { name: 'attic' }],
    interfaces: [{ name: 'eth1', zone: 'lan' }],
    aliases: [{ name: 'spare_hosts', type: 'hosts', entries: ['192.0.2.7'] }],
    schedules: [{ name: 'night_shift' }],
    rules: [
      { id: 'r1', zone: 'attic', action: 'accept' },
      { id: 'r2', zone: 'lan', action: 'accept', enabled: false, description: 'Old printer' },
    ],
    nat: { outbound: { rules: [{ id: 'o1', zone: 'lan', enabled: false }] } },
    protection: { zones: ['lan', 'attic'] },
  }
}

const found = {
  unused: [
    {
      kind: 'alias',
      id: 'spare_hosts',
      name: 'spare_hosts',
      path: 'aliases[spare_hosts]',
      why: 'Nothing names it',
    },
    {
      kind: 'schedule',
      id: 'night_shift',
      name: 'night_shift',
      path: 'schedules[night_shift]',
      why: 'No rule names it',
    },
    {
      kind: 'zone',
      id: 'attic',
      name: 'attic',
      path: 'zones[attic]',
      why: 'No interface is in it',
      takes: ['rule r1', 'protection'],
    },
  ],
  disabled: [
    { kind: 'rule', id: 'r2', name: 'Old printer', path: 'rules[r2]' },
    { kind: 'outbound-nat', id: 'o1', name: 'o1', path: 'nat.outbound.rules[o1]' },
  ],
}

async function open({ role = 'admin', answer = found, cfg = draft() } = {}) {
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  config.replaceDraft(cfg)
  if (answer instanceof Error) api.config.unused.mockRejectedValue(answer)
  else api.config.unused.mockResolvedValue(answer)
  const wrapper = mount(UnusedSection, { global: { stubs: { RouterLink: RouterLinkStub } } })
  await flushPromises()
  return { wrapper, config }
}

const nameOf = (tr) => {
  const td = tr.find('td[data-label="Name"]')
  return td.exists() ? td.text() : ''
}
const rowOf = (wrapper, name) => wrapper.findAll('tbody tr').find((tr) => nameOf(tr) === name)
const button = (wrapper, label) => wrapper.findAll('button').find((b) => b.text() === label)

describe('UnusedSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('asks about the draft and lists both groups', async () => {
    const { wrapper, config } = await open()
    expect(api.config.unused).toHaveBeenCalledWith(config.draft)
    const groups = wrapper.findAll('th[scope="rowgroup"]').map((th) => th.text())
    expect(groups).toEqual(['Unused', 'Disabled'])

    const alias = rowOf(wrapper, 'spare_hosts')
    expect(alias.get('td[data-label="Kind"]').text()).toBe('Alias')
    expect(alias.get('td[data-label="Why"]').text()).toBe('Nothing names it')
    expect(alias.getComponent(RouterLinkStub).props('to')).toBe('/firewall/aliases')

    const zone = rowOf(wrapper, 'attic')
    expect(zone.get('td[data-label="Kind"]').text()).toBe('Zone')
    expect(zone.get('td[data-label="Why"]').text()).toContain('Takes rule r1, protection')
    expect(zone.getComponent(RouterLinkStub).props('to')).toBe('/interfaces#zones')

    const rule = wrapper.findAll('tbody tr').find((tr) => nameOf(tr).startsWith('Old printer'))
    expect(rule.get('td[data-label="Kind"]').text()).toBe('Rule')
    expect(rule.get('td[data-label="Name"]').text()).toContain('r2')
    expect(rule.get('td[data-label="Why"]').text()).toBe('Switched off')
    expect(rule.getComponent(RouterLinkStub).props('to')).toBe('/firewall/rules#lan')

    const outbound = rowOf(wrapper, 'o1')
    expect(outbound.get('td[data-label="Kind"]').text()).toBe('Outbound rule')
    expect(outbound.getComponent(RouterLinkStub).props('to')).toBe('/firewall/nat')
  })

  it('removes every unused item in one go, with one way back', async () => {
    const { wrapper, config } = await open()
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const remove = button(wrapper, 'Remove 3 unused items')
    expect(remove).toBeTruthy()
    await remove.trigger('click')
    await flushPromises()

    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({
        question: 'Remove 3 unused items?',
        dependents: [
          'alias spare_hosts',
          'schedule night_shift',
          'zone attic',
          'rule r1',
          'protection',
        ],
        dependentsLabel: 'Removed',
        typed: '',
      }),
    )
    expect(config.aliases).toEqual([])
    expect(config.draft.schedules).toEqual([])
    expect(config.zones.map((z) => z.name)).toEqual(['lan'])
    expect(config.rules.map((r) => r.id)).toEqual(['r2'])
    expect(config.draft.protection.zones).toEqual(['lan'])

    const toasts = useToastStore().toasts
    expect(toasts.map((t) => t.message)).toEqual(['Removed 3 unused items.'])
    expect(rowOf(wrapper, 'spare_hosts')).toBeUndefined()
    expect(wrapper.text()).toContain('No unused items.')

    toasts[0].action.run()
    expect(config.draft).toEqual(draft())
  })

  it('deletes one item with its own question', async () => {
    const { wrapper, config } = await open()
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const zone = rowOf(wrapper, 'attic')
    await button(zone, 'Delete').trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({ question: 'Delete zone attic?', typed: 'attic' }),
    )
    expect(ask.mock.calls[0][0].dependents).toEqual(['rule r1', 'protection'])
    expect(config.zones.map((z) => z.name)).toEqual(['lan'])
    expect(rowOf(wrapper, 'attic')).toBeUndefined()
    expect(useToastStore().toasts.map((t) => t.message)).toEqual(['Deleted zone attic.'])
  })

  // A list read before the draft changed can name what is in use by now.
  it('leaves out of the removal what its own Delete would refuse', async () => {
    const cfg = draft()
    cfg.interfaces.push({ name: 'eth2', zone: 'attic' })
    const { wrapper, config } = await open({ cfg })
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    expect(button(rowOf(wrapper, 'attic'), 'Delete').attributes('disabled')).toBeDefined()
    await button(wrapper, 'Remove 2 unused items').trigger('click')
    await flushPromises()

    expect(ask.mock.calls[0][0].dependents).toEqual(['alias spare_hosts', 'schedule night_shift'])
    expect(config.aliases).toEqual([])
    expect(config.draft.schedules).toEqual([])
    expect(config.zones.map((z) => z.name)).toEqual(['lan', 'attic'])
    expect(config.rules.map((r) => r.id)).toEqual(['r1', 'r2'])
    expect(useToastStore().toasts.map((t) => t.message)).toEqual(['Removed 2 unused items.'])
    expect(rowOf(wrapper, 'attic')).toBeTruthy()
  })

  it('holds the removal until the list has caught up with the draft', async () => {
    vi.useFakeTimers()
    try {
      const { wrapper, config } = await open()
      const remove = () => button(wrapper, 'Remove 3 unused items')
      expect(remove().attributes('disabled')).toBeUndefined()

      let answer
      api.config.unused.mockReturnValue(new Promise((resolve) => (answer = resolve)))
      config.upsertRule({ id: 'r3', zone: 'lan', action: 'accept' })
      await flushPromises()
      expect(remove().attributes('disabled')).toBeDefined()
      vi.advanceTimersByTime(400)
      await flushPromises()
      expect(api.config.unused).toHaveBeenCalledTimes(2)
      expect(remove().attributes('disabled')).toBeDefined()

      answer(found)
      await flushPromises()
      expect(remove().attributes('disabled')).toBeUndefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it('says when there is nothing in either group', async () => {
    const { wrapper } = await open({ answer: { unused: [], disabled: [] } })
    expect(wrapper.text()).toContain('No unused items.')
    expect(wrapper.text()).toContain('No disabled items.')
    expect(wrapper.findAll('button').some((b) => b.text().startsWith('Remove'))).toBe(false)
  })

  it('says why the list is empty when the request fails', async () => {
    const { wrapper } = await open({ answer: new Error('the configuration does not parse') })
    expect(wrapper.get('[role="alert"]').text()).toBe('the configuration does not parse')
    expect(wrapper.text()).toContain('No unused items.')
    expect(wrapper.text()).not.toContain('Reading…')
  })

  it('leaves out every action for a viewer', async () => {
    const { wrapper } = await open({ role: 'viewer' })
    expect(rowOf(wrapper, 'spare_hosts')).toBeTruthy()
    expect(wrapper.findAll('button')).toHaveLength(0)
  })

  it('asks again a moment after the draft changes', async () => {
    vi.useFakeTimers()
    try {
      const { config } = await open()
      expect(api.config.unused).toHaveBeenCalledTimes(1)
      config.removeAlias('spare_hosts')
      await flushPromises()
      vi.advanceTimersByTime(400)
      await flushPromises()
      expect(api.config.unused).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })
})
