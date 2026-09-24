import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import RemoteBackupSection from '@/views/system/RemoteBackupSection.vue'

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual('@/lib/api')
  return {
    ApiError: actual.ApiError,
    api: {
      config: { remoteCopies: vi.fn(), restoreRemote: vi.fn() },
      crons: { list: vi.fn(), run: vi.fn() },
    },
  }
})

const remote = {
  enabled: true,
  endpoint: 'https://s3.us-west-004.backblazeb2.com',
  bucket: 'router-backups',
  keyId: '0055abc',
  secret: 'not-a-real-key',
  passphrase: 'correct horse',
}

const config = (backup) => ({
  version: 3,
  system: {},
  zones: [],
  interfaces: [],
  rules: [],
  ...(backup ? { backup } : {}),
})

const copies = [
  {
    key: 'ostiole/fw-20260920-030000.json.age',
    name: 'fw-20260920-030000.json.age',
    hostname: 'fw',
    takenAt: '2026-09-20T03:00:00Z',
    size: 21504,
  },
  {
    key: 'ostiole/fw-20260919-030000.json.age',
    name: 'fw-20260919-030000.json.age',
    hostname: 'fw',
    takenAt: '2026-09-19T03:00:00Z',
    size: 20480,
  },
]

/** The prefix, the schedule and retention sit behind the Advanced fold. */
async function openAdvanced(wrapper) {
  await wrapper
    .findAll('button')
    .find((b) => b.text().includes('Advanced'))
    .trigger('click')
  await flushPromises()
}

/** Mounts the section with the given block saved and in the draft. */
async function open(backup = { remote }, role = 'admin') {
  useAuthStore().user = { username: role, role }
  const store = useConfigStore()
  store.saved = config(backup)
  store.replaceDraft(config(backup))
  const wrapper = mount(RemoteBackupSection)
  await flushPromises()
  return { wrapper, store }
}

describe('RemoteBackupSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.config.remoteCopies.mockResolvedValue({ enabled: true, copies })
    api.crons.list.mockResolvedValue([
      {
        id: 'system:remote-backup',
        lastRun: '2026-09-20T03:00:00Z',
        lastOutput: 'uploaded ostiole/fw-20260920-030000.json.age (21 KB)',
      },
    ])
    api.crons.run.mockResolvedValue({ id: 'system:remote-backup' })
  })

  it('shows what the draft holds', async () => {
    const { wrapper } = await open()
    expect(wrapper.get('#rb-endpoint').element.value).toBe(remote.endpoint)
    expect(wrapper.get('#rb-bucket').element.value).toBe('router-backups')
    expect(wrapper.get('#rb-enabled').element.checked).toBe(true)
    // An empty schedule shows the default rather than nothing.
    await openAdvanced(wrapper)
    expect(wrapper.get('#rb-schedule').element.value).toBe('0 3 * * *')
  })

  // The bucket gets the account hashes, so where it is and how it is
  // locked are an admin's to say. An operator can still read the settings.
  it('keeps the settings from an operator', async () => {
    const { wrapper } = await open({ remote }, 'operator')
    expect(wrapper.get('#rb-enabled').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('Only an admin can change these.')
    await openAdvanced(wrapper)
    const fieldsets = wrapper.findAll('fieldset')
    expect(fieldsets).toHaveLength(2)
    for (const f of fieldsets) expect(f.attributes('disabled')).toBeDefined()
    expect(wrapper.get('#rb-endpoint').element.value).toBe(remote.endpoint)
  })

  it('writes a typed endpoint into the draft', async () => {
    const { wrapper, store } = await open()
    await wrapper.get('#rb-endpoint').setValue('https://s3.us-east-005.backblazeb2.com')
    expect(store.draft.backup.remote.endpoint).toBe('https://s3.us-east-005.backblazeb2.com')
    await openAdvanced(wrapper)
    await wrapper.get('#rb-keep').setValue('3')
    expect(store.draft.backup.remote.keep).toBe(3)
  })

  it('lists the copies newest first with their sizes', async () => {
    const { wrapper } = await open()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('fw-20260920-030000.json.age')
    expect(rows[0].text()).toContain('21 KB')
    expect(wrapper.text()).toContain('uploaded ostiole/fw-20260920-030000.json.age')
  })

  it('restores one copy and shows what it would change', async () => {
    api.config.restoreRemote.mockResolvedValue({
      summary: {
        hostname: 'fw',
        createdAt: '2026-09-20T03:00:00Z',
        zones: 2,
        interfaces: 2,
        rules: 3,
        aliases: 0,
        gateways: 1,
      },
      config: config(),
      changes: [],
    })
    const { wrapper } = await open()
    await wrapper.findAll('tbody tr')[0].get('button').trigger('click')
    await flushPromises()
    expect(api.config.restoreRemote).toHaveBeenCalledWith('ostiole/fw-20260920-030000.json.age', '')
    expect(wrapper.get('[role="note"]').text()).toContain('What it would change')
  })

  // An older copy may have been locked with another passphrase; that is
  // the one failure worth asking about rather than reporting.
  it('asks for a copy’s own passphrase when the configured one does not open it', async () => {
    api.config.restoreRemote.mockRejectedValueOnce(
      new ApiError(400, 'the passphrase does not open this backup'),
    )
    const { wrapper } = await open()
    const row = () => wrapper.findAll('tbody tr')[0]
    await row().get('button').trigger('click')
    await flushPromises()
    const field = row().get('input[type="password"]')
    expect(field.attributes('aria-label')).toContain('fw-20260920-030000.json.age')

    api.config.restoreRemote.mockResolvedValue({
      summary: {
        hostname: 'fw',
        createdAt: '2026-09-20T03:00:00Z',
        zones: 2,
        interfaces: 2,
        rules: 3,
        aliases: 0,
        gateways: 1,
      },
      config: config(),
      changes: [],
    })
    await field.setValue('an older one')
    await row().get('button').trigger('click')
    await flushPromises()
    expect(api.config.restoreRemote).toHaveBeenLastCalledWith(
      'ostiole/fw-20260920-030000.json.age',
      'an older one',
    )
  })

  it('backs up now through the cron and reads the result back', async () => {
    const { wrapper } = await open()
    await wrapper
      .findAll('button')
      .find((b) => b.text().includes('Back up now'))
      .trigger('click')
    await flushPromises()
    expect(api.crons.run).toHaveBeenCalledWith('system:remote-backup')
    expect(api.crons.list).toHaveBeenCalledTimes(2)
    expect(api.config.remoteCopies).toHaveBeenCalledTimes(2)
  })

  // The form stays, so the bucket can be filled in before it is switched
  // on; the status and the copies follow what is applied.
  it('shows the form but no bucket when the saved configuration has it off', async () => {
    const { wrapper } = await open({ remote: { ...remote, enabled: false } })
    expect(wrapper.get('#rb-bucket').element.value).toBe('router-backups')
    expect(wrapper.find('tbody').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Back up now')
    expect(api.crons.list).not.toHaveBeenCalled()
    expect(api.config.remoteCopies).not.toHaveBeenCalled()
  })
})
