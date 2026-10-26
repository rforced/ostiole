import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import { NAV } from '@/lib/nav'
import { useAuthStore } from '@/stores/auth'
import ConfigurationPage from '@/views/system/ConfigurationPage.vue'
import { sessionOf, must } from '@/test/fixtures'

const { tabs } = must(
  must(must(NAV.find((i) => i.to === '/system')).pages).find((p) => p.path === 'configuration'),
)

/** Mounts the page at a path for a role, with the sections stubbed. */
async function page(path: string, role: string) {
  useAuthStore().user = sessionOf({ username: role, role })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/system/configuration', component: ConfigurationPage, meta: { tabs } }],
  })
  router.push(path)
  await router.isReady()
  const wrapper = mount(ConfigurationPage, {
    global: {
      plugins: [router],
      stubs: {
        RevisionsSection: true,
        BackupSection: true,
        RemoteBackupSection: true,
        UnusedSection: true,
      },
    },
  })
  await flushPromises()
  return wrapper
}

const tabNames = (wrapper: VueWrapper) => wrapper.findAll('[role="tab"]').map((t) => t.text())

describe('ConfigurationPage', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('opens on the history', async () => {
    const wrapper = await page('/system/configuration', 'admin')
    expect(tabNames(wrapper)).toEqual(['History', 'Backup', 'Remote backup', 'Unused'])
    expect(wrapper.find('revisions-section-stub').exists()).toBe(true)
  })

  it('opens the tab a link names', async () => {
    const wrapper = await page('/system/configuration#backup', 'operator')
    expect(wrapper.find('backup-section-stub').exists()).toBe(true)
  })

  // A viewer can neither download nor restore, so the tab would be empty.
  it('has no backup tab for a viewer, and a link to it opens the history', async () => {
    const wrapper = await page('/system/configuration#backup', 'viewer')
    expect(tabNames(wrapper)).toEqual(['History', 'Remote backup', 'Unused'])
    expect(wrapper.find('backup-section-stub').exists()).toBe(false)
    expect(wrapper.find('revisions-section-stub').exists()).toBe(true)
  })
})
