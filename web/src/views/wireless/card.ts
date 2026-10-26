import type { Band, Standard } from '@/lib/model.gen'

export interface CardChannel {
  number: number
  mhz: number
  radar?: boolean
  disabled?: boolean
}

export interface CardBand {
  channels: CardChannel[]
  maxWidth: number
  standards: Standard[]
  serves: boolean
}

/** One device as /wireless/radios reports it. */
export interface Card {
  name: string
  phy?: string
  driver?: string
  mac?: string
  selfManaged?: boolean
  maxNetworks: number
  bands: Partial<Record<Band, CardBand>>
  running?: {
    channel: number
    width: number
    txPower: number
    networks: { interface: string; ssid?: string; clients: number }[]
  }
  stopped?: { reason: string }
}

/** One station as /wireless/clients reports it. */
export interface Client {
  mac: string
  interface: string
  ssid?: string
  radio: string
  hostname?: string
  address?: string
  signalDbm: number
  connectedSeconds: number
  inactiveMs: number
  rxBytes: number
  txBytes: number
  txBitrate?: string
  rxBitrate?: string
}
