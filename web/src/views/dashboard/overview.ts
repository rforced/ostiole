import type { FwlogEntry } from '@/lib/fwlog'

/** The parts of a GET /overview reply the dashboard cards draw. */

export interface Warning {
  kind: string
  level: string
  title: string
  detail?: string
}

export interface LinkSummary {
  name: string
  zone?: string
  description?: string
  kind?: string
  configured: boolean
  enabled: boolean
  present: boolean
  up: boolean
  carrier: boolean
  external?: boolean
  hostSide?: boolean
  mtu?: number
  mac?: string
  vlanId?: number
  wireless?: boolean
  ipv4Mode?: string
  ipv6Mode?: string
  addresses: string[] | null
  configuredAddresses?: string[]
  rxBytes: number
  txBytes: number
  rxErrors?: number
  txErrors?: number
}

export interface RuleCounter {
  id: string
  description?: string
  zone: string
  action: string
  enabled: boolean
  packets: number
  bytes: number
}

export interface ServiceState {
  name: string
  unit?: string
  state: string
  want: boolean
  detail?: string
}

export interface FamilyStatus {
  family: string
  address?: string
  online: boolean
  unknown: boolean
  neverAnswered?: boolean
  latencyMs: number
  lossPercent: number
  lastError?: string
}

export interface Over {
  family: string
  latencyMs?: number
  lossPercent?: number
  limit: number
  span: number
  since: string
}

export interface GatewayStatus {
  name: string
  interface: string
  address?: string
  monitor?: string
  priority: number
  metric: number
  online: boolean
  unknown: boolean
  neverAnswered?: boolean
  active: boolean
  tunnel?: boolean
  latencyMs: number
  lossPercent: number
  since?: string
  lastError?: string
  families?: FamilyStatus[]
  slow?: Over[]
  lossy?: Over[]
  span: number
}

export interface DetectedGateway {
  address: string
  interface: string
  family: string
  metric: number
  protocol: string
  configured?: string
  disabled?: string
  demoted?: boolean
}

/** One gateway of GET /gateways/strips, as far as the cards read it. */
export interface GatewayStripData {
  name: string
  monitor?: string
}

/** A recent drop, as the firewall log holds it. */
export type BlockEntry = Omit<FwlogEntry, 'seq'> & { seq?: number }

export interface Lease {
  expires?: string
  mac?: string
  ip: string
  hostname?: string
  clientId?: string
  family: number
  interface?: string
  static: boolean
  description?: string
  renewed?: string
  seen?: string
  online?: boolean
}
