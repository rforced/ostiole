import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import RulesetPage from '@/views/diagnostics/RulesetPage.vue'

vi.mock('@/lib/api', () => ({
  api: { host: { status: vi.fn(), flushLegacy: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = { ConfirmButton: true, RefreshButton: true, RulesetSection: true }

/** A router with one leftover table nothing owns and one that is Docker's. */
function report(over = {}) {
  return {
    root: true,
    legacy: {
      tables: [
        { backend: 'nft', family: 'ip', name: 'filter', rules: 0, chains: ['INPUT'] },
        {
          backend: 'nft',
          family: 'ip',
          name: 'nat',
          rules: 0,
          chains: ['DOCKER'],
          owner: 'Docker',
        },
      ],
    },
    ...over,
  }
}

async function page(over = {}) {
  api.host.status.mockResolvedValue(report(over))
  const wrapper = mount(RulesetPage, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('RulesetPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('lists the leftover rules below the rendered configuration', async () => {
    const wrapper = await page()
    const html = wrapper.html()
    expect(html.indexOf('ruleset-section-stub')).toBeGreaterThan(-1)
    expect(html.indexOf('Leftover rules')).toBeGreaterThan(html.indexOf('ruleset-section-stub'))
    expect(wrapper.text()).toContain('ip filter')
    expect(wrapper.text()).toContain('Belongs to Docker, so it is left alone.')
  })

  it('clears the leftovers nothing else owns, and offers the owned one separately', async () => {
    const wrapper = await page()
    api.host.flushLegacy.mockResolvedValue({ output: 'cleared ip filter', status: report() })
    const buttons = wrapper.findAllComponents({ name: 'ConfirmButton' })
    // One sweep for the unowned table, one "clear anyway" for Docker's.
    expect(buttons).toHaveLength(2)
    expect(buttons[0].props('label')).toBe('Clear leftovers')
    expect(buttons[1].props('typed')).toBe('nat')
    buttons[0].vm.$emit('confirm')
    await flushPromises()
    expect(api.host.flushLegacy).toHaveBeenCalledWith([])
    expect(wrapper.text()).toContain('cleared ip filter')
  })

  it('offers nothing to press on a daemon that is not root', async () => {
    const wrapper = await page({ root: false })
    expect(wrapper.text()).toContain('ip filter')
    expect(wrapper.findAllComponents({ name: 'ConfirmButton' })).toHaveLength(0)
  })

  it('shows why a flush failed', async () => {
    const wrapper = await page()
    api.host.flushLegacy.mockRejectedValue(new Error('not permitted'))
    wrapper.findAllComponents({ name: 'ConfirmButton' })[0].vm.$emit('confirm')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('not permitted')
  })

  it('reads the kernel again on Refresh, and says why a read failed', async () => {
    const wrapper = await page()
    api.host.status.mockRejectedValue(new Error('nft is not installed'))
    wrapper.findComponent({ name: 'RefreshButton' }).vm.$emit('click')
    await flushPromises()
    expect(api.host.status).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').text()).toContain('nft is not installed')
  })
})
