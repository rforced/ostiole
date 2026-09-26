import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import CronsPage from '@/views/system/CronsPage.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class extends Error {},
  api: { config: { get: vi.fn() }, crons: { list: vi.fn(), run: vi.fn() } },
}))

const config = {
  version: 7,
  zones: [],
  interfaces: [],
  rules: [],
  system: {},
  crons: [
    { id: 'nightly', enabled: true, schedule: '0 4 * * *', kind: 'backup', directory: '/b' },
    {
      id: 'hashes',
      enabled: true,
      schedule: '0 5 * * *',
      kind: 'backup',
      directory: '/b',
      withUsers: true,
    },
    { id: 'script', enabled: true, schedule: '0 6 * * *', kind: 'command', command: '/bin/true' },
    { id: 'morning', enabled: true, schedule: '0 7 * * *', kind: 'wake', device: 'wol-nas' },
  ],
  services: {
    wol: {
      devices: [{ id: 'wol-nas', interface: 'eth1', mac: 'aa:bb:cc:00:00:01', description: 'NAS' }],
    },
  },
}

async function open(role) {
  useAuthStore().user = { username: role, role }
  api.config.get.mockResolvedValue(structuredClone(config))
  api.crons.list.mockResolvedValue([])
  const wrapper = mount(CronsPage, { global: { stubs: { CronDialog: true } } })
  await flushPromises()
  return wrapper
}

const rows = (w) => w.findAll('tbody tr').filter((tr) => tr.find('button').exists())
const button = (tr, label) => tr.findAll('button').find((b) => b.text().includes(label))

describe('CronsPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // A cron that runs a command runs it as root, and one that backs up the
  // accounts writes their hashes out: the apply refuses a change to either
  // from anyone but an admin, so the page does not offer one.
  it('keeps the crons only an admin may change from an operator', async () => {
    const w = await open('operator')
    const [nightly, hashes, script] = rows(w)
    expect(button(nightly, 'Edit').attributes('disabled')).toBeUndefined()
    expect(button(hashes, 'Edit').attributes('disabled')).toBeDefined()
    expect(button(hashes, 'Delete').attributes('disabled')).toBeDefined()
    // Running the backup now changes nothing; running the command does.
    expect(button(hashes, 'Run now').attributes('disabled')).toBeUndefined()
    expect(button(script, 'Run now').attributes('disabled')).toBeDefined()
    expect(button(script, 'Edit').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('Only an admin can change cron jobs that run a command')
  })

  it('offers an admin every cron', async () => {
    const w = await open('admin')
    expect(rows(w)).toHaveLength(4)
    for (const tr of rows(w)) {
      for (const label of ['Run now', 'Edit', 'Delete'])
        expect(button(tr, label).attributes('disabled')).toBeUndefined()
    }
    expect(w.text()).not.toContain('Only an admin')
  })

  it('names the device a wake cron wakes', async () => {
    const w = await open('operator')
    const morning = rows(w)[3]
    expect(morning.text()).toContain('Wake NAS')
    expect(button(morning, 'Edit').attributes('disabled')).toBeUndefined()
  })
})
