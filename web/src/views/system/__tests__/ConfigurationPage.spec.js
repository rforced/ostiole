import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import { NAV } from '@/lib/nav'
import { useAuthStore } from '@/stores/auth'
import ConfigurationPage from '@/views/system/ConfigurationPage.vue'

const { tabs } = NAV.find((i) => i.to === '/system').pages.find((p) => p.path === 'configuration')

/** Mounts the page at a path for a role, with the sections stubbed. */
async function page(path, role) {
  useAuthStore().user = { username: role, role }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/system/configuration', component: ConfigurationPage, meta: { tabs } }],
  })
  router.push(path)
  await router.isReady()
  const wrapper = mount(ConfigurationPage, {
    global: {
      plugins: [router],
      stubs: { RevisionsSection: true, BackupSection: true, RemoteBackupSection: true },
    },
  })
  await flushPromises()
  return wrapper
}

const tabNames = (wrapper) => wrapper.findAll('[role="tab"]').map((t) => t.text())

describe('ConfigurationPage', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('opens on the history', async () => {
    const wrapper = await page('/system/configuration', 'admin')
    expect(tabNames(wrapper)).toEqual(['History', 'Backup', 'Remote backup'])
    expect(wrapper.find('revisions-section-stub').exists()).toBe(true)
  })

  it('opens the tab a link names', async () => {
    const wrapper = await page('/system/configuration#backup', 'operator')
    expect(wrapper.find('backup-section-stub').exists()).toBe(true)
  })

  // A viewer can neither download nor restore, so the tab would be empty.
  it('has no backup tab for a viewer, and a link to it opens the history', async () => {
    const wrapper = await page('/system/configuration#backup', 'viewer')
    expect(tabNames(wrapper)).toEqual(['History', 'Remote backup'])
    expect(wrapper.find('backup-section-stub').exists()).toBe(false)
    expect(wrapper.find('revisions-section-stub').exists()).toBe(true)
  })
})
