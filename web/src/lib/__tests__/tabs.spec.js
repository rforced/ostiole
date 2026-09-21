import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import { usePageTabs, useTabHash } from '@/lib/tabs'

const TABS = ['rules', 'aliases', 'nat']

const Host = defineComponent({
  setup() {
    return { tab: useTabHash(TABS) }
  },
  render() {
    return h('span', this.tab)
  },
})

async function mountAt(path) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/firewall', component: Host }],
  })
  router.push(path)
  await router.isReady()
  return { wrapper: mount(Host, { global: { plugins: [router] } }), router }
}

describe('useTabHash', () => {
  it('reads the tab from the hash', async () => {
    const { wrapper } = await mountAt('/firewall#nat')
    expect(wrapper.text()).toBe('nat')
  })

  it('falls back to the first tab without a hash, or with one nobody knows', async () => {
    expect((await mountAt('/firewall')).wrapper.text()).toBe('rules')
    expect((await mountAt('/firewall#nonsense')).wrapper.text()).toBe('rules')
  })

  it('writes the hash when the tab changes, and clears it for the default', async () => {
    const { wrapper, router } = await mountAt('/firewall')
    wrapper.vm.tab = 'aliases'
    await flushPromises()
    expect(router.currentRoute.value.hash).toBe('#aliases')
    expect(wrapper.text()).toBe('aliases')

    wrapper.vm.tab = 'rules'
    await flushPromises()
    expect(router.currentRoute.value.hash).toBe('')
    expect(router.currentRoute.value.fullPath).toBe('/firewall')
  })

  it('ignores a value that is not a tab', async () => {
    const { wrapper, router } = await mountAt('/firewall#nat')
    wrapper.vm.tab = 'nope'
    await flushPromises()
    expect(router.currentRoute.value.hash).toBe('#nat')
  })

  it('honours the hash once a list the configuration supplies arrives', async () => {
    const zones = ref([])
    const Late = {
      setup() {
        return { zone: useTabHash(() => zones.value) }
      },
      render() {
        return h('span', this.zone)
      },
    }
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/firewall/rules', component: Late }],
    })
    router.push('/firewall/rules#dmz')
    await router.isReady()
    const wrapper = mount(Late, { global: { plugins: [router] } })
    // Nothing loaded is nothing to choose, rather than a guess at the hash.
    expect(wrapper.text()).toBe('')
    zones.value = ['lan', 'wan', 'dmz']
    await nextTick()
    expect(wrapper.text()).toBe('dmz')
    // And a zone that goes away takes you to the first one that is left.
    zones.value = ['lan', 'wan']
    await nextTick()
    expect(wrapper.text()).toBe('lan')
  })
})

describe('usePageTabs', () => {
  it('reads the tabs the route declares and opens the one in the hash', async () => {
    const Page = {
      setup() {
        return usePageTabs()
      },
      render() {
        return h('span', `${this.tabs.map((t) => t.label).join(',')}:${this.tab}`)
      },
    }
    const tabs = [
      { value: 'v4', label: 'IPv4' },
      { value: 'v6', label: 'IPv6' },
    ]
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/services/dhcp', component: Page, meta: { tabs } }],
    })
    router.push('/services/dhcp#v6')
    await router.isReady()
    const wrapper = mount(Page, { global: { plugins: [router] } })
    expect(wrapper.text()).toBe('IPv4,IPv6:v6')
  })
})
