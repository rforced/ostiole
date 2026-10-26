/** A gateway as the monitor reports it. */
export interface GatewayStatus {
  name: string
  online: boolean
  unknown: boolean
}

/** An OpenVPN tunnel as its daemon reports it. */
export interface OvpnLive {
  name: string
  running: boolean
  connected: boolean
  dataChannel: string
  bytesIn: number
  bytesOut: number
  note: string
}

export interface OvpnStatus {
  setUp: boolean
  secureBootSkipped: boolean
  tunnels: OvpnLive[] | null
}

/** A WireGuard peer as its tunnel's device reports it. */
export interface WgLivePeer {
  name?: string
  publicKey: string
  endpoint?: string
  lastHandshake?: string
  rxBytes: number
  txBytes: number
}

/** A WireGuard tunnel as the kernel reports it. */
export interface WgLive {
  name: string
  up: boolean
  listenPort?: number
  peers: WgLivePeer[]
}

/** A tailnet peer as the daemon reports it. */
export interface TailscalePeer {
  hostName: string
  dnsName?: string
  os?: string
  ips: string[]
  routes: string[]
  online: boolean
  lastSeen?: string
  active: boolean
  relay?: string
  directAddr?: string
  rxBytes?: number
  txBytes?: number
  exitNode?: boolean
  expired?: boolean
}

/** The node's state as the daemon reports it. */
export interface TailscaleNodeStatus {
  setUp: boolean
  running: boolean
  state?: string
  authUrl?: string
  peers: TailscalePeer[]
}
