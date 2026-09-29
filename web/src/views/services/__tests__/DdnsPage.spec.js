import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import DdnsPage from '@/views/services/DdnsPage.vue'
import RecordDialog from '@/views/services/ddns/RecordDialog.vue'

vi.mock('@/lib/api', () => ({
  api: { ddns: { status: vi.fn(), update: vi.fn(), check: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const KINDS = [{ kind: 'cloudflare', label: 'Cloudflare', dynamicDns: true, fields: [] }]

const stubs = {
  ConfirmButton: { props: ['question', 'description'], template: '<span class="confirm" />' },
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div v-if="open" class="dialog"><slot /></div>',
  },
}

function config() {
  return {
    version: 11,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true },
      { name: 'eth1', zone: 'lan', enabled: true },
    ],
    rules: [],
    dnsProviders: [
      { id: 'cf', kind: 'cloudflare', settings: { token: 't' }, domains: ['example.com'] },
    ],
    services: {
      ddns: {
        records: [
          {
            id: 'ddns-home',
            enabled: true,
            name: 'home.example.com',
            interface: 'eth0',
            ipv4: true,
            ipv6: true,
          },
          { id: 'ddns-new', enabled: true, name: 'new.example.com', interface: 'eth0', ipv4: true },
        ],
      },
    },
  }
}

const STATUS = {
  kinds: KINDS,
  records: [
    {
      id: 'ddns-home',
      name: 'home.example.com',
      type: 'A',
      state: 'current',
      address: '203.0.113.7',
      published: ['203.0.113.7'],
      changedAt: '2026-09-26T12:00:00Z',
    },
    {
      id: 'ddns-home',
      name: 'home.example.com',
      type: 'AAAA',
      state: 'failed',
      address: '2001:db8::7',
      error: 'Cloudflare refused the token for example.com (10000 Authentication error)',
    },
  ],
}

async function page(role = 'admin', draft = config()) {
  useAuthStore().user = { username: role, role }
  const store = useConfigStore()
  store.draft = draft
  store.saved = config()
  store.loaded = true
  api.ddns.status.mockResolvedValue(STATUS)
  const wrapper = mount(DdnsPage, { global: { stubs } })
  await flushPromises()
  return { wrapper, store }
}

const row = (w, text) => w.findAll('tbody tr').find((tr) => tr.text().includes(text))
/** Text with the non-breaking spaces that keep a type beside its value. */
const plain = (el) => el.text().replaceAll('\u00a0', ' ')
const button = (el, label) => el.findAll('button').find((b) => b.text().trim() === label)

describe('DdnsPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('shows each record type as the router last found it', async () => {
    const { wrapper } = await page()
    const home = row(wrapper, 'home.example.com')
    expect(plain(home)).toContain('A 203.0.113.7')
    expect(home.text()).toContain('current')
    expect(home.text()).toContain('failed')
    expect(home.text()).toContain('refused the token for example.com')
    // Only in the draft: nothing has started it.
    expect(row(wrapper, 'new.example.com').text()).toContain('apply to start')
    expect(
      button(row(wrapper, 'new.example.com'), 'Update now').attributes('disabled'),
    ).toBeDefined()
    const confirm = row(wrapper, 'home.example.com').findComponent('.confirm')
    expect(confirm.props('description')).toBe('The record at Cloudflare stays as it is.')
  })

  it('names the DNS provider that writes each record', async () => {
    const draft = config()
    draft.dnsProviders[0].domains = ['home.example.com']
    draft.dnsProviders.push({ id: 'cloudflare', kind: 'cloudflare', domains: ['new.example.com'] })
    draft.services.ddns.records.push({
      id: 'ddns-www',
      enabled: true,
      name: 'www.example.net',
      interface: 'eth0',
      ipv4: true,
    })
    const { wrapper } = await page('admin', draft)
    const provider = (name) => row(wrapper, name).get('td[data-label="DNS provider"]')
    const lines = (name) => provider(name).findAll('div')
    expect(lines('home.example.com').map((d) => d.text())).toEqual(['cf', 'Cloudflare'])
    // Named for its kind, so the kind is not said twice.
    expect(lines('new.example.com').map((d) => d.text())).toEqual(['cloudflare'])
    expect(provider('www.example.net').text()).toBe('—')
  })

  it('asks for an update now, and polls while the provider is asked', async () => {
    const { wrapper } = await page('operator')
    api.ddns.update.mockResolvedValue({ id: 'ddns-home' })
    await button(row(wrapper, 'home.example.com'), 'Update now').trigger('click')
    await flushPromises()
    expect(api.ddns.update).toHaveBeenCalledWith('ddns-home')
    expect(api.ddns.status).toHaveBeenCalledTimes(2)
  })

  it('points at the providers page until one can keep a record', async () => {
    const draft = config()
    draft.dnsProviders[0].domains = []
    const { wrapper } = await page('admin', draft)
    expect(wrapper.text()).toContain('No DNS provider can keep a record yet')
    expect(wrapper.find('a[href="/system/dns-providers"]').exists()).toBe(true)
  })

  it('gives a viewer the list and nothing that acts', async () => {
    const { wrapper } = await page('viewer')
    expect(button(wrapper, 'Add record')).toBeUndefined()
    expect(button(row(wrapper, 'home.example.com'), 'Update now')).toBeUndefined()
    expect(button(row(wrapper, 'home.example.com'), 'View')).toBeDefined()
  })
})

function dialog(record = null, role = 'admin') {
  useAuthStore().user = { username: role, role }
  const store = useConfigStore()
  store.draft = config()
  store.saved = config()
  store.loaded = true
  const wrapper = mount(RecordDialog, {
    props: { open: true, record, kinds: KINDS },
    global: { stubs },
  })
  return { wrapper, store }
}

const save = (w) => w.get('form').trigger('submit')

describe('RecordDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('names the provider that writes the record, and saves it to the draft', async () => {
    const { wrapper, store } = dialog()
    await wrapper.get('#ddns-name').setValue('NAS.Example.com.')
    expect(wrapper.text()).toContain('Written through cf (Cloudflare).')
    expect(wrapper.get('#ddns-if').element.value).toBe('eth0')
    await wrapper.findAll('input[type="checkbox"]')[2].setValue(true)
    await save(wrapper)
    expect(store.ddnsRecords.at(-1)).toMatchObject({
      enabled: true,
      name: 'nas.example.com',
      interface: 'eth0',
      ipv4: true,
      ipv6: true,
    })
    expect(store.ddnsRecords.at(-1).id).toMatch(/^ddns-/)
  })

  it('refuses a name no provider holds, a record with no type, and a type kept twice', async () => {
    const { wrapper, store } = dialog()
    await wrapper.get('#ddns-name').setValue('home.example.net')
    expect(wrapper.text()).toContain('No DNS provider holds home.example.net.')
    await save(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toContain('Add its domain to one')

    await wrapper.get('#ddns-name').setValue('home.example.com')
    await save(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'Another record keeps the A record for home.example.com.',
    )

    await wrapper.findAll('input[type="checkbox"]')[1].setValue(false)
    await save(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('Keep the A record, the AAAA record or both.')
    expect(store.ddnsRecords).toHaveLength(2)
  })

  it('checks a record against the provider and says what an apply would do', async () => {
    const { wrapper } = dialog()
    await wrapper.get('#ddns-name').setValue('nas.example.com')
    await wrapper.findAll('input[type="checkbox"]')[2].setValue(true)
    api.ddns.check.mockResolvedValue([
      { type: 'A', address: '203.0.113.7', published: ['198.51.100.5'], action: 'change' },
      { type: 'AAAA', published: [], action: 'no-address' },
    ])
    await button(wrapper, 'Check').trigger('click')
    await flushPromises()
    const [, record] = api.ddns.check.mock.calls[0]
    expect(record).toMatchObject({ name: 'nas.example.com', ipv4: true, ipv6: true })
    const status = plain(wrapper.get('[role="status"]'))
    expect(status).toContain(
      'A: Cloudflare holds 198.51.100.5. The apply changes it to 203.0.113.7.',
    )
    expect(status).toContain('AAAA: eth0 has no public IPv6 address, so Cloudflare keeps nothing.')
  })
})
