import { describe, expect, it } from 'vitest'

import table from '../../../../internal/model/testdata/provider-for.json'
import { certificateProvider, providerFor } from '@/lib/providers'

describe('providerFor', () => {
  it.each(table.cases)('finds the provider for $name as the server does', (tc) => {
    const got = providerFor(table.providers, tc.name)
    expect({ provider: got?.provider.id ?? '', zone: got?.zone ?? '' }).toEqual({
      provider: tc.provider,
      zone: tc.zone,
    })
  })
})

describe('certificateProvider', () => {
  const providers = [
    { id: 'cf', domains: ['example.com'] },
    { id: 'home', domains: ['home.example.com'] },
  ]

  it.each([
    [{ names: ['example.com', '*.example.com'] }, 'cf', ''],
    [{ names: ['nas.home.example.com'] }, 'home', ''],
    [
      { names: ['example.com', 'nas.home.example.com'] },
      '',
      'example.com is on cf and nas.home.example.com on home; name the provider that writes both.',
    ],
    [{ names: ['example.com', 'nas.home.example.com'], provider: 'cf' }, 'cf', ''],
    [{ names: ['example.net'] }, '', 'No DNS provider holds example.net.'],
    [{ names: ['example.com'], provider: 'gone' }, '', 'Unknown DNS provider gone.'],
    [{ names: [] }, '', ''],
  ])('finds the provider for %j', (cert, id, problem) => {
    const got = certificateProvider(providers, cert)
    expect({ id: got.provider?.id ?? '', problem: got.problem }).toEqual({ id, problem })
  })
})
