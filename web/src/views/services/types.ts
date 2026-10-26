import type { DiscoveryEvent as Packet } from '@/lib/discovery'

/** One DHCP lease as the router reports it. */
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

/** One message of a DHCP client's, from the server's log. */
export interface DhcpEvent {
  seq: number
  time: string
  message: string
  interface: string
  address?: string
  device?: string
  mac?: string
  duid?: string
  name?: string
  detail?: string
}

/** One packet the discovery relay saw, with the time the log stamps it. */
export interface DiscoveryEvent extends Packet {
  time: string
}

/** A service a device said it offers. */
export interface Announcement {
  protocol: string
  interface: string
  type: string
  name: string
  host?: string
  port?: number
  addresses?: string[]
  location?: string
  lastSeen: string
  expires: string
}

/** One request the proxy answered. */
export interface ProxyRequest {
  seq: number
  time: string
  site?: string
  client: string
  method: string
  host: string
  path: string
  proto?: string
  status: number
  bytes: number
  duration: number
  agent?: string
  by?: string
}

/** One match of a WAF rule. */
export interface WafHit {
  id: number
  message?: string
  data?: string
  severity?: string
}

/** One transaction the WAF had something to say about. */
export interface WafEvent {
  seq: number
  time: string
  logged: string
  id: string
  site?: string
  client?: string
  method?: string
  uri?: string
  status?: number
  verdict: string
  engine?: string
  rules: WafHit[] | null
  moreMatches?: number
}

/** One answer in the DNS query log. */
export interface QueryRow {
  seq: number
  time: string
  client: string
  device?: string
  name: string
  type: string
  status: string
  reason?: string
  lists?: string[]
  answer?: string
}

/** A DNS provider kind that can keep a dynamic DNS record. */
export interface DdnsKind {
  kind: string
  label: string
}

/** How one record type of a dynamic DNS record is doing. */
export interface DdnsStatus {
  id: string
  name: string
  type: string
  interface: string
  provider?: string
  state: string
  address?: string
  published?: string[]
  checkedAt?: string
  changedAt?: string
  error?: string
  nextTry?: string
}
