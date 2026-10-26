import type { Point } from '@/lib/traffic'

export interface Totals {
  down: number
  up: number
}

/** One link or device in a stream event: its rate now. */
export interface Rate {
  id: string
  down: number
  up: number
}

export interface TrafficEvent {
  kind: 'links' | 'devices'
  time: string
  interval?: number
  links?: Rate[]
  devices?: Rate[]
}

export interface Link {
  name: string
  down: number
  up: number
  totals: Totals
  since: string
  points?: Point[]
  description?: string
  zone?: string
  external?: boolean
  configured: boolean
}

export interface Links {
  window: string
  now: string
  links: Link[]
}

export interface Device {
  id: string
  name?: string
  mac?: string
  router?: boolean
  addresses: string[] | null
  interface?: string
  down: number
  up: number
  totals: Totals
  since: string
  lastSeen: string
  points?: Point[]
}

export interface Devices {
  window: string
  now: string
  devices: Device[]
  counting: boolean
  since?: string
  interval?: number
  error?: string
}

export interface Destination {
  device: string
  deviceName?: string
  destination: string
  address?: string
  protocol: string
  port?: number
  service?: string
  down: number
  up: number
  connections: number
  lastSeen: string
}

export interface Destinations {
  window: string
  enabled: boolean
  entries: Destination[] | null
  next?: number
  more: boolean
  held: number
  oldest?: string
}
