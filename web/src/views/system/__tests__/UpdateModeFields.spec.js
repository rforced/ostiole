import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import UpdateModeFields from '@/views/system/UpdateModeFields.vue'

const open = (props = {}) => mount(UpdateModeFields, { props: { prefix: 'upd', ...props } })

describe('UpdateModeFields', () => {
  // Checking and installing are two different questions, and the defaults
  // are the ones the daemon fills in for a router that says nothing.
  it('offers a schedule for each, defaulting to what the daemon does', () => {
    const w = open()
    expect(w.get('#upd-check-schedule').element.value).toBe('0 4 * * *')
    expect(w.get('#upd-install-schedule').element.value).toBe('30 4 * * 0')
    // Both defaults are presets rather than "Something else".
    expect(w.get('#upd-check-preset').element.value).toBe('0 4 * * *')
    expect(w.get('#upd-install-preset').element.value).toBe('30 4 * * 0')
  })

  it('emits each schedule on its own', async () => {
    const w = open({ checkSchedule: '0 2 * * *', installSchedule: '0 3 * * 1' })
    expect(w.get('#upd-check-schedule').element.value).toBe('0 2 * * *')
    expect(w.get('#upd-install-schedule').element.value).toBe('0 3 * * 1')

    await w.get('#upd-check-schedule').setValue('15 1 * * *')
    await w.get('#upd-check-schedule').trigger('change')
    expect(w.emitted('update:checkSchedule').at(-1)).toEqual(['15 1 * * *'])
    expect(w.emitted('update:installSchedule')).toBeUndefined()

    await w.get('#upd-install-preset').setValue('0 4 1 * *')
    expect(w.emitted('update:installSchedule').at(-1)).toEqual(['0 4 1 * *'])
  })

  // A router that installs by hand has nothing to schedule an install
  // for, but it still asks what is waiting.
  it('drops the install row on manual and says so', () => {
    const w = open({ mode: 'manual' })
    expect(w.find('#upd-install-schedule').exists()).toBe(false)
    expect(w.find('#upd-install-preset').exists()).toBe(false)
    expect(w.get('#upd-check-schedule').exists()).toBe(true)
    expect(w.text()).toContain('Nothing is installed on a schedule')
  })

  it('brings the install row back when a mode installs again', async () => {
    const w = open({ mode: 'manual' })
    await w.setProps({ mode: 'security' })
    expect(w.find('#upd-install-schedule').exists()).toBe(true)
    expect(w.text()).not.toContain('Nothing is installed on a schedule')
  })

  it('greys out security where the package manager has no such channel', () => {
    const w = open({ securityCapable: false, securityNote: 'pacman has no security-only channel.' })
    expect(w.get('#upd-mode-security').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('pacman has no security-only channel.')
  })
})
