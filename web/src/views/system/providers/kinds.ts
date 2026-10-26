export interface ProviderField {
  key: string
  label: string
  secret?: boolean
  required?: boolean
  hint?: string
}

export interface ProviderKind {
  kind: string
  label: string
  fields: ProviderField[]
  dynamicDns?: boolean
}

export interface ProviderTest {
  zones?: string[]
  more?: number
  domains?: { domain: string; error?: string }[]
}
