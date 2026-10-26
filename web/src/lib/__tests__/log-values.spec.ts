import { describe, expect, it } from 'vitest'

import type { AuditEvent } from '@/lib/api'
import { auditValues } from '@/lib/audit'
import { dhcpValues, type DhcpEvent } from '@/lib/dhcpLog'
import { readingValues, type DriveReading } from '@/lib/driveHistory'
import { eventValues as gatewayValues, type GatewayEvent } from '@/lib/gateways'
import { fwlogValues, type FwlogEntry } from '@/lib/fwlog'
import { peerValues, type PeerEvent } from '@/lib/peerLog'
import { eventValues, type WafEvent } from '@/lib/proxyEvents'
import { requestValues, type ProxyRequest } from '@/lib/proxyRequests'
import { wirelessValues, type WirelessEvent } from '@/lib/wirelessLog'
import { queryValues, type QueryRow } from '@/lib/queries'

import { fixture } from '@/test/fixtures'

import cases from './log-values.json'

/** The values as the search sees them: empty ones are skipped. */
const shown = (values: unknown[]) =>
  values.filter((v) => v !== undefined && v !== null && v !== '').map(String)

// The router searches a log by the same values its page shows; the Go tests
// read these cases too.
describe('what each log row shows', () => {
  for (const c of cases.firewall) {
    it(`firewall: ${c.why}`, () => {
      expect(shown(fwlogValues(fixture<FwlogEntry>(c.entry), c.access ?? {}))).toEqual(c.values)
    })
  }
  for (const c of cases.queries) {
    it(`queries: ${c.why}`, () => {
      expect(shown(queryValues(fixture<QueryRow>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.events) {
    it(`events: ${c.why}`, () => {
      expect(shown(eventValues(fixture<WafEvent>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.requests) {
    it(`requests: ${c.why}`, () => {
      expect(shown(requestValues(fixture<ProxyRequest>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.dhcp) {
    it(`dhcp: ${c.why}`, () => {
      expect(shown(dhcpValues(fixture<DhcpEvent>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.wireless) {
    it(`wireless: ${c.why}`, () => {
      expect(shown(wirelessValues(fixture<WirelessEvent>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.peers) {
    it(`peers: ${c.why}`, () => {
      expect(shown(peerValues(fixture<PeerEvent>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.drives) {
    it(`drives: ${c.why}`, () => {
      expect(shown(readingValues(fixture<DriveReading>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.gateways) {
    it(`gateways: ${c.why}`, () => {
      expect(shown(gatewayValues(fixture<GatewayEvent>(c.entry)))).toEqual(c.values)
    })
  }
  for (const c of cases.audit) {
    it(`audit: ${c.why}`, () => {
      expect(shown(auditValues(fixture<AuditEvent>(c.entry)))).toEqual(c.values)
    })
  }
})
