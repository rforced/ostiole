import { describe, expect, it } from 'vitest'

import { LOGS, fullBytes, logSettings, totalBytes } from '@/lib/logs'

const serving = {
  enabled: true,
  sites: [{ id: 'shop', enabled: true }],
}

describe('logs', () => {
  it('fills in the defaults', () => {
    const s = logSettings({})
    expect(s.firewall).toEqual({ on: true, entries: LOGS.firewall.entries })
    expect(s.queries.on).toBe(false)
    expect(s.events.on).toBe(false)
    expect(fullBytes('events')).toBe(LOGS.events.entries * LOGS.events.bytes)
  })

  // The proxy's requests cost memory only at the levels that keep them.
  it('counts the requests at Info and Debug', () => {
    const cfg = { services: { proxy: { ...serving, requests: { entries: 4000 } } } }
    expect(logSettings(cfg).requests.on).toBe(false)
    cfg.system = { logging: { level: 'info' } }
    expect(logSettings(cfg).requests).toEqual({ on: true, entries: 4000 })
    expect(totalBytes(cfg)).toBe(
      LOGS.firewall.entries * 350 + LOGS.events.entries * 1536 + 4000 * 400,
    )
  })

  it('counts the DHCP log while the server is on at Info and Debug', () => {
    const cfg = { services: { dhcp: { enabled: true, log: { entries: 500 } } } }
    expect(logSettings(cfg).dhcp.on).toBe(false)
    cfg.system = { logging: { level: 'debug' } }
    expect(logSettings(cfg).dhcp).toEqual({ on: true, entries: 500 })
  })

  // A query log or a proxy that is off costs nothing, whatever it is set to.
  it('counts only the logs that are on', () => {
    const cfg = {
      system: { management: { firewallLog: { entries: 1000 } } },
      services: {
        dns: { enabled: true, queryLog: { enabled: true, entries: 2000 } },
        proxy: { ...serving, events: { entries: 3000 } },
      },
    }
    expect(totalBytes(cfg)).toBe(1000 * 350 + 2000 * 150 + 3000 * 1536)
    cfg.services.dns.enabled = false
    cfg.services.proxy.enabled = false
    expect(totalBytes(cfg)).toBe(1000 * 350)
  })
})
