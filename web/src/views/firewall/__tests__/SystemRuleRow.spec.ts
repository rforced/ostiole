import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { SystemRule } from '@/lib/api'
import { must } from '@/test/fixtures'
import SystemRuleRow from '@/views/firewall/SystemRuleRow.vue'

const replies = {
  chain: 'input',
  action: 'accept',
  protocol: 'any',
  source: 'any',
  destination: 'any',
  description: 'Replies and related traffic of connections already allowed',
}

const lockout = {
  chain: 'input',
  zones: ['lan'],
  action: 'accept',
  protocol: 'tcp',
  source: 'any',
  destination: 'this router : 443, 22',
  description: 'Anti-lockout, keeps the web UI and SSH reachable',
  keys: ['input/anti-lockout:lan'],
  setting: 'zone',
}

const badges = (rule: SystemRule) =>
  mount(SystemRuleRow, { props: { rule }, global: { stubs: { RouterLink: true } } }).findAll(
    '.badge',
  )

describe('SystemRuleRow zones', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('marks a row that names no zone as counting all of them', () => {
    const found = badges({ ...replies, log: true })
    const log = must(found[1])
    const every = must(found[2])
    expect(every.text()).toBe('all')
    expect(every.classes()).toEqual(log.classes())
    expect(badges({ ...replies, zones: [] }).map((b) => b.text())).toEqual(['accept', 'all'])
  })

  it('leaves the mark off a row that names its zones', () => {
    expect(badges(lockout).map((b) => b.text())).toEqual(['accept'])
  })
})
