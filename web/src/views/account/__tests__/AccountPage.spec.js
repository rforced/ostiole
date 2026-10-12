import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import AccountPage from '@/views/account/AccountPage.vue'

const stubs = { PasswordCard: true, MfaCard: true, TokensCard: true, SessionsCard: true }

describe('AccountPage', () => {
  it('heads the page Account and stacks the four cards in order', () => {
    const wrapper = mount(AccountPage, { global: { stubs } })
    expect(wrapper.get('h1').text()).toBe('Account')
    const cards = wrapper
      .findAll('password-card-stub, mfa-card-stub, tokens-card-stub, sessions-card-stub')
      .map((w) => w.element.tagName.toLowerCase())
    expect(cards).toEqual([
      'password-card-stub',
      'mfa-card-stub',
      'tokens-card-stub',
      'sessions-card-stub',
    ])
  })
})
