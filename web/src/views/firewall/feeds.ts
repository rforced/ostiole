/** What GET /aliases/feeds says about one fetched alias. */

export interface FeedPart {
  source: string
  country?: string
  asn?: string
  holder?: string
  entries: number
}

export interface FeedStatus {
  alias: string
  sources: string[]
  parts?: FeedPart[]
  entries: number
  fetchedAt?: string
  lastError?: string
  lastTriedAt?: string
  stale: boolean
}
