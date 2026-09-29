import { describe, expect, it } from 'vitest'

import { dhcpValues } from '@/lib/dhcpLog'
import { readingValues } from '@/lib/driveHistory'
import { fwlogValues } from '@/lib/fwlog'
import { peerValues } from '@/lib/peerLog'
import { eventValues } from '@/lib/proxyEvents'
import { requestValues } from '@/lib/proxyRequests'
import { wirelessValues } from '@/lib/wirelessLog'
import { queryValues } from '@/lib/queries'

import cases from './log-values.json'

/** The values as the search sees them: empty ones are skipped. */
const shown = (values) =>
  values.filter((v) => v !== undefined && v !== null && v !== '').map(String)

// The router searches a log by the same values its page shows; the Go tests
// read these cases too.
describe('what each log row shows', () => {
  for (const c of cases.firewall) {
    it(`firewall: ${c.why}`, () => {
      expect(shown(fwlogValues(c.entry, c.access ?? {}))).toEqual(c.values)
    })
  }
  for (const c of cases.queries) {
    it(`queries: ${c.why}`, () => {
      expect(shown(queryValues(c.entry))).toEqual(c.values)
    })
  }
  for (const c of cases.events) {
    it(`events: ${c.why}`, () => {
      expect(shown(eventValues(c.entry))).toEqual(c.values)
    })
  }
  for (const c of cases.requests) {
    it(`requests: ${c.why}`, () => {
      expect(shown(requestValues(c.entry))).toEqual(c.values)
    })
  }
  for (const c of cases.dhcp) {
    it(`dhcp: ${c.why}`, () => {
      expect(shown(dhcpValues(c.entry))).toEqual(c.values)
    })
  }
  for (const c of cases.wireless) {
    it(`wireless: ${c.why}`, () => {
      expect(shown(wirelessValues(c.entry))).toEqual(c.values)
    })
  }
  for (const c of cases.peers) {
    it(`peers: ${c.why}`, () => {
      expect(shown(peerValues(c.entry))).toEqual(c.values)
    })
  }
  for (const c of cases.drives) {
    it(`drives: ${c.why}`, () => {
      expect(shown(readingValues(c.entry))).toEqual(c.values)
    })
  }
})
