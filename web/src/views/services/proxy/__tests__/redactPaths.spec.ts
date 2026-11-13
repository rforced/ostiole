import { describe, expect, it } from 'vitest'

import { redactPathsProblem } from '@/views/services/proxy/redactPaths'

describe('redactPathsProblem', () => {
  it.each([
    [['/s/', '/feed/', '/a-b.c_d~e/f/'], ''],
    [['/s/'.padEnd(47, 'a') + '/'], ''],
    [[], ''],
    [['/'], '"/" must start and end with / and hold at least 3 characters'],
    [['//'], '"//" must start and end with / and hold at least 3 characters'],
    [['s/'], '"s/" must start and end with / and hold at least 3 characters'],
    [['/s'], '"/s" must start and end with / and hold at least 3 characters'],
    [
      ['/s/'.padEnd(48, 'a') + '/'],
      '"' + '/s/'.padEnd(48, 'a') + '/" is longer than 48 characters',
    ],
    [['/€€€€€€€€€€€€€€€€/'], '"/€€€€€€€€€€€€€€€€/" is longer than 48 characters'],
    [['/a b/'], '"/a b/" may hold only letters, digits and - . _ ~ /'],
    [['/a%2Fb/'], '"/a%2Fb/" may hold only letters, digits and - . _ ~ /'],
    [['/a//b/'], '"/a//b/" must not hold //'],
    [['/s/', '/s/'], 'duplicate prefix "/s/"'],
  ])('%j gives %j', (prefixes, problem) => {
    expect(redactPathsProblem(prefixes)).toBe(problem)
  })

  it('caps the different prefixes across the proxy, each counted once', () => {
    const others = Array.from({ length: 9 }, (_, i) => `/p${i}/`)
    expect(redactPathsProblem(['/p0/', '/new/'], others)).toBe('')
    expect(redactPathsProblem(['/new/', '/more/'], others)).toBe(
      'the proxy hides at most 10 different prefixes across its sites',
    )
  })
})
