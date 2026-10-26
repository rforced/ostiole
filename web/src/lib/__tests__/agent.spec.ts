import { describe, expect, it } from 'vitest'

import { describe as describeAgent } from '@/lib/agent'

const AGENTS = {
  'Firefox on Linux': 'Mozilla/5.0 (X11; Linux x86_64; rv:141.0) Gecko/20100101 Firefox/141.0',
  'Firefox on Windows':
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:141.0) Gecko/20100101 Firefox/141.0',
  'Firefox on Android': 'Mozilla/5.0 (Android 15; Mobile; rv:141.0) Gecko/141.0 Firefox/141.0',
  'Firefox on iPhone':
    'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/141.0 Mobile/15E148 Safari/605.1.15',
  'Chrome on Android':
    'Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Mobile Safari/537.36',
  'Chrome on macOS':
    'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36',
  'Chrome on Linux':
    'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36',
  'Chrome on ChromeOS':
    'Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36',
  'Chrome on iPhone':
    'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/139.0.0.0 Mobile/15E148 Safari/604.1',
  'Safari on iPhone':
    'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1',
  'Safari on iPad':
    'Mozilla/5.0 (iPad; CPU OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1',
  'Safari on macOS':
    'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Safari/605.1.15',
  'Edge on Windows':
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36 Edg/139.0.0.0',
  'Edge on Android':
    'Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Mobile Safari/537.36 EdgA/139.0.0.0',
  'Opera on Windows':
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36 OPR/121.0.0.0',
  'Samsung Internet on Android':
    'Mozilla/5.0 (Linux; Android 15; SM-X000) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/28.0 Chrome/130.0.0.0 Mobile Safari/537.36',
}

describe('describe', () => {
  for (const [want, agent] of Object.entries(AGENTS)) {
    it(`reads ${want}`, () => {
      expect(describeAgent(agent)).toBe(want)
    })
  }

  it('names the browser alone when the system is not known', () => {
    expect(describeAgent('Mozilla/5.0 (Plan 9; rv:141.0) Gecko/20100101 Firefox/141.0')).toBe(
      'Firefox',
    )
  })

  it('shows an agent it cannot read as it came', () => {
    expect(describeAgent('curl/8.15.0')).toBe('curl/8.15.0')
  })

  it('cuts a long unread agent to 40 characters', () => {
    const got = describeAgent('kettle-sync/2.4.1 (+https://example.com/tools/sync; nightly)')
    expect(got).toBe('kettle-sync/2.4.1 (+https://example.com…')
    expect(got).toHaveLength(40)
  })

  it('says unknown when there is no agent', () => {
    expect(describeAgent('')).toBe('unknown')
    expect(describeAgent('   ')).toBe('unknown')
    expect(describeAgent(undefined)).toBe('unknown')
  })
})
