import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useToastStore } from '@/stores/toast'
import ExclusionDialog from '@/views/services/proxy/ExclusionDialog.vue'
import ExclusionsCard from '@/views/services/proxy/ExclusionsCard.vue'

vi.mock('@/lib/api', () => ({ api: {}, ApiError: class ApiError extends Error {} }))

const dialogStub = { AppDialog: { template: '<div><slot /><slot name="footer" /></div>' } }

function config(profiles) {
  return {
    version: 11,
    zones: [],
    interfaces: [],
    rules: [],
    services: { proxy: { enabled: true, wafProfiles: profiles } },
  }
}

function store(profiles, saved = profiles) {
  const s = useConfigStore()
  s.draft = config(structuredClone(profiles))
  s.saved = config(structuredClone(saved))
  s.loaded = true
  return s
}

async function card(profiles, saved) {
  const s = store(profiles, saved)
  const wrapper = mount(ExclusionsCard, {
    global: { stubs: { ConfirmButton: true, ExclusionDialog: true } },
  })
  await flushPromises()
  return { wrapper, store: s }
}

const rows = (w) => w.findAll('tbody tr')
const cells = (w) => rows(w).map((r) => r.findAll('td').map((td) => td.text()))

describe('ExclusionsCard', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('shows each exclusion in full, and what an empty path or variable means', async () => {
    const { wrapper } = await card([
      {
        id: 'watch',
        exclusions: [
          { rule: '942100', description: 'SQL Injection Attack Detected via libinjection' },
          {
            rule: '942430',
            path: '/ocs/v2.php/apps/notifications/api/v2/notifications',
            target: 'ARGS:q',
          },
        ],
      },
    ])
    expect(cells(wrapper)).toEqual([
      [
        '942100SQL Injection Attack Detected via libinjection',
        'every path',
        'every variable',
        'Edit',
      ],
      ['942430', '/ocs/v2.php/apps/notifications/api/v2/notifications', 'ARGS:q', 'Edit'],
    ])
  })

  it("keeps the rules in order, one rule's paths together", async () => {
    const { wrapper } = await card([
      {
        id: 'watch',
        exclusions: [
          { rule: '942200' },
          { rule: '941100', path: '/z' },
          { rule: '941000-941999', path: '/notes' },
          { rule: '941100', path: '/a' },
        ],
      },
    ])
    expect(cells(wrapper).map((c) => `${c[0]} ${c[1]}`)).toEqual([
      '941000-941999 /notes',
      '941100 /a',
      '941100 /z',
      '942200 every path',
    ])
  })

  it('finds an exclusion by any of its fields', async () => {
    const { wrapper } = await card([
      {
        id: 'watch',
        exclusions: [
          { rule: '942100', description: 'SQL Injection' },
          { rule: '941100', target: 'ARGS:content' },
          { rule: '920420', path: '/Sessions/Playing' },
        ],
      },
    ])
    const search = wrapper.find('input[type=search]')
    for (const [query, rule] of [
      ['injection', '942100'],
      ['args:content', '941100'],
      ['/sessions', '920420'],
    ]) {
      await search.setValue(query)
      expect(cells(wrapper).map((c) => c[0].slice(0, 6))).toEqual([rule])
    }
    expect(wrapper.text()).toContain('1 of 3')
    await search.setValue('nothing')
    expect(cells(wrapper)).toEqual([['Nothing matches "nothing".']])
  })

  it('shows one profile at a time, and offers the choice only when there is one', async () => {
    const one = await card([{ id: 'watch' }])
    expect(one.wrapper.find('select[aria-label="WAF profile"]').exists()).toBe(false)
    expect(cells(one.wrapper)).toEqual([['No exclusions.']])

    setActivePinia(createPinia())
    const { wrapper } = await card([
      { id: 'watch', exclusions: [{ rule: '942100' }] },
      { id: 'vault', exclusions: [{ rule: '941100' }, { rule: '920420' }] },
    ])
    const picker = wrapper.get('select[aria-label="WAF profile"]')
    expect(cells(wrapper).map((c) => c[0])).toEqual(['942100'])
    await picker.setValue('vault')
    expect(cells(wrapper).map((c) => c[0])).toEqual(['920420', '941100'])
    expect(wrapper.findComponent(ExclusionDialog).props('profile')).toBe('vault')
  })

  it('marks what the draft adds or changes, and nothing it leaves', async () => {
    const saved = [
      { id: 'watch', exclusions: [{ rule: '941100' }, { rule: '942100', path: '/a' }] },
    ]
    const draft = [
      {
        id: 'watch',
        // Deleting the first one moves the rest up; they are unchanged.
        exclusions: [
          { rule: '942100', path: '/a' },
          { rule: '942200' },
          { rule: '920420', description: 'new' },
        ],
      },
    ]
    const { wrapper } = await card(draft, saved)
    const marked = rows(wrapper).map((r) => r.classes('row-changed'))
    expect(cells(wrapper).map((c) => c[0])).toEqual(['920420new', '942100', '942200'])
    expect(marked).toEqual([true, false, true])
  })

  it('edits and deletes the exclusion in its row, whatever the order', async () => {
    const { wrapper, store: s } = await card([
      { id: 'watch', exclusions: [{ rule: '942200' }, { rule: '941100', path: '/api' }] },
    ])
    // Sorted, the first row is the second exclusion.
    await rows(wrapper)[0].get('button.link-action').trigger('click')
    expect(wrapper.findComponent(ExclusionDialog).props()).toMatchObject({
      open: true,
      index: 1,
      exclusion: { rule: '941100', path: '/api' },
    })
    const del = wrapper.findAllComponents(ConfirmButton)[0]
    expect(del.props('question')).toBe('Delete exclusion rule 941100 on /api?')
    del.vm.$emit('confirm')
    expect(s.proxy.wafProfiles[0].exclusions).toEqual([{ rule: '942200' }])
  })

  it('lets a viewer read them and change nothing', async () => {
    useAuthStore().user = { username: 'eve', role: 'viewer' }
    const { wrapper } = await card([{ id: 'watch', exclusions: [{ rule: '942100' }] }])
    expect(wrapper.text()).not.toContain('Add exclusion')
    expect(rows(wrapper)[0].get('button.link-action').text()).toBe('View')
  })
})

function dialog(props, profiles = [{ id: 'watch', exclusions: [] }]) {
  const s = store(profiles)
  const wrapper = mount(ExclusionDialog, {
    props: { open: true, profile: 'watch', ...props },
    global: { stubs: dialogStub },
  })
  return { wrapper, store: s }
}

const saveButton = (w) => w.get('button[type=submit]')

describe('ExclusionDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('starts from what it is given, and adds only what was filled in', async () => {
    const { wrapper, store: s } = dialog({
      exclusion: { rule: '941100', path: '/Items/1b2c/PlaybackInfo', description: 'XSS' },
    })
    await flushPromises()
    expect(wrapper.get('#exc-rule').element.value).toBe('941100')
    expect(wrapper.get('#exc-path').element.value).toBe('/Items/1b2c/PlaybackInfo')
    expect(wrapper.get('#exc-target').element.value).toBe('')
    // No example in the empty fields: an example there reads as a value.
    expect(wrapper.get('#exc-path').attributes('placeholder')).toBeUndefined()
    expect(wrapper.get('#exc-target').attributes('placeholder')).toBeUndefined()
    await wrapper.get('#exc-path').setValue(' /Items ')
    await wrapper.get('form').trigger('submit')
    expect(s.proxy.wafProfiles[0].exclusions).toEqual([
      { rule: '941100', path: '/Items', description: 'XSS' },
    ])
    expect(useToastStore().toasts.at(-1).message).toBe(
      'Excluded rule 941100 on /Items in the draft.',
    )
    expect(wrapper.emitted('update:open')).toEqual([[false]])
  })

  it('writes an edit back to its place', async () => {
    const { wrapper, store: s } = dialog({ exclusion: { rule: '942100', path: '/a' }, index: 1 }, [
      { id: 'watch', exclusions: [{ rule: '941100' }, { rule: '942100', path: '/a' }] },
    ])
    await flushPromises()
    expect(wrapper.text()).not.toContain('That exclusion is in the profile already.')
    await wrapper.get('#exc-target').setValue('ARGS:q')
    await wrapper.get('form').trigger('submit')
    expect(s.proxy.wafProfiles[0].exclusions).toEqual([
      { rule: '941100' },
      { rule: '942100', path: '/a', target: 'ARGS:q' },
    ])
  })

  it('says what the router would refuse, and saves nothing until it is fixed', async () => {
    const { wrapper } = dialog({}, [{ id: 'watch', exclusions: [{ rule: '942100', path: '/a' }] }])
    await flushPromises()
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()
    for (const [field, value, message] of [
      ['#exc-rule', 'abc', 'A rule is an ID or a range like 942100-942199.'],
      ['#exc-rule', '0', 'Rule IDs start at 1.'],
      ['#exc-rule', '942199-942100', 'The range ends before it starts.'],
      ['#exc-path', 'api', 'A path starts with /.'],
      ['#exc-path', '/a"b', 'A path cannot hold a quote or a backslash.'],
      ['#exc-path', '/a\tb', 'A path cannot hold a control character.'],
      ['#exc-target', 'args:q', 'A variable is written like ARGS or ARGS:password.'],
      ['#exc-path', '/a', 'That exclusion is in the profile already.'],
    ]) {
      await wrapper.get('#exc-rule').setValue(field === '#exc-rule' ? value : '942100')
      await wrapper.get('#exc-path').setValue(field === '#exc-path' ? value : '/b')
      await wrapper.get('#exc-target').setValue(field === '#exc-target' ? value : '')
      expect(wrapper.get('[role=alert]').text()).toBe(message)
      expect(saveButton(wrapper).attributes('disabled')).toBeDefined()
    }
    await wrapper.get('#exc-path').setValue('/b')
    expect(wrapper.find('[role=alert]').exists()).toBe(false)
    expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()
  })
})
